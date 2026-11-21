package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/store/postgres"
)

const candidateReceiverPath = "/internal/candidate-messages"

// Injected by the release image build.
var receiverSourceSHA string
var receiverRollbackRef = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`)

type receiverRequest struct {
	Operation string
	postgres.CandidateRunKey
	Binding                      *postgres.CandidateRun
	Source, Lease, Text          string
	Revision                     int
	StopTransfer, RevokeOutbound bool
}

type receiverReply struct {
	ReceiverSHA, ReceiverRollback string
	Message                       *orchestrator.CandidateMessage `json:",omitempty"`
	Delivery                      *chat.DeliveryStatus           `json:",omitempty"`
}

type candidateReceiver struct {
	store  *postgres.CandidateMessages
	chat   *chat.Service
	source interface {
		CheckShare(context.Context, drive.Ref, string) error
	}
	roles           *access.Service
	token, rollback string
}

func (s candidateReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Values("Authorization")
	if r.TLS == nil || len(token) != 1 || subtle.ConstantTimeCompare([]byte(token[0]), []byte("Bearer "+s.token)) != 1 ||
		r.Header.Get("X-Omnirelay-Receiver-SHA") != receiverSourceSHA || r.Header.Get("X-Omnirelay-Receiver-Rollback") != s.rollback {
		http.NotFound(w, r)
		return
	}
	var req receiverRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "无效的受控消息请求", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 28*time.Second)
	defer cancel()
	result, err := s.execute(ctx, req)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, postgres.ErrCandidateScope) || errors.Is(err, chat.ErrAccessLost) || errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusForbidden
		}
		http.Error(w, "受控消息操作未完成", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	result.ReceiverSHA = receiverSourceSHA
	result.ReceiverRollback = s.rollback
	_ = json.NewEncoder(w).Encode(result)
}

func (s candidateReceiver) execute(ctx context.Context, req receiverRequest) (receiverReply, error) {
	var reply receiverReply
	key := req.CandidateRunKey
	switch req.Operation {
	case "status":
		return reply, nil
	case "open":
		if req.Binding == nil {
			return reply, postgres.ErrCandidateScope
		}
		binding := *req.Binding
		binding.CandidateRunKey = key
		actor, err := s.store.BindingActor(ctx, binding)
		if err != nil {
			return reply, err
		}
		allowed, err := s.roles.Check(ctx, actor, "project.read", binding.Project)
		if err != nil {
			return reply, err
		}
		if !allowed {
			return reply, postgres.ErrCandidateScope
		}
		err = s.store.Open(ctx, binding)
		if err == nil {
			slog.InfoContext(ctx, "候选消息授权已启用", "run", key.Run, "candidate_sha", key.SHA, "receiver_sha", receiverSourceSHA)
		}
		return reply, err
	case "stop":
		return reply, s.store.Stop(ctx, key, req.StopTransfer, req.RevokeOutbound)
	case orchestrator.CandidateClaim:
		item, file, err := s.store.Claim(ctx, key)
		if err != nil || item == nil {
			return reply, err
		}
		check, stop := context.WithTimeout(ctx, 12*time.Second)
		defer stop()
		if err := s.checkClaim(check, key, item, file); err != nil {
			return reply, err
		}
		if err := s.store.ClaimCurrent(check, key, item); err != nil {
			return reply, err
		}
		reply.Message = item
		return reply, nil
	case orchestrator.CandidateAck:
		return reply, s.store.Ack(ctx, key, req.Source, req.Lease, req.Revision)
	case orchestrator.CandidateReply, orchestrator.CandidateReceipt:
		writes := req.Operation == orchestrator.CandidateReply
		if writes && (strings.TrimSpace(req.Text) == "" || len(req.Text) > 8192) {
			return reply, postgres.ErrCandidateScope
		}
		if err := s.store.ReserveOutbound(ctx, key, req.Source, writes); err != nil {
			return reply, err
		}
		err := s.store.WithOutbound(ctx, key, req.Source, writes, func(out postgres.CandidateOutbound) error {
			call, done := context.WithDeadline(ctx, out.Expires)
			defer done()
			authorize := func(call context.Context) error {
				action := "project.read"
				if writes {
					action = "project.comment"
				}
				ok, err := s.roles.Check(call, out.Actor, action, out.Project)
				if err != nil {
					return err
				}
				if !ok {
					return postgres.ErrCandidateScope
				}
				return s.store.OutboundCurrent(call, key, req.Source, writes)
			}
			if err := authorize(call); err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(key.Run + "\x00" + req.Source))
			op := "candidate-" + hex.EncodeToString(digest[:])
			var status chat.DeliveryStatus
			var err error
			if writes {
				status, err = s.chat.SendCandidateReply(call, out.Actor, out.Chat, op, req.Text, authorize)
			} else {
				status, err = s.chat.CandidateReplyReceipt(call, out.Actor, out.Chat, op)
			}
			if err == nil {
				reply.Delivery = &status
			}
			return err
		})
		return reply, err
	default:
		return reply, postgres.ErrCandidateScope
	}
}

func (s candidateReceiver) checkClaim(ctx context.Context, key postgres.CandidateRunKey, item *orchestrator.CandidateMessage, file string) error {
	e := &item.Event
	a := e.Archive
	// Deliver only the deletion marker when removing previously delivered data.
	if a != nil && a.Message != nil && a.Message.Deleted {
		return nil
	}
	ctx, cancel := context.WithDeadline(ctx, item.Expires)
	defer cancel()
	actor, err := s.store.CandidateActor(ctx, key)
	if err != nil {
		return err
	}
	ok, err := s.roles.Check(ctx, actor, "project.read", item.Project)
	if err != nil || !ok {
		if err == nil {
			err = postgres.ErrCandidateScope
		}
		return err
	}
	target := e.ChatID
	if a != nil {
		target = a.ChatID
	}
	if err := s.chat.VerifyCandidateConversation(ctx, actor, target); err != nil {
		return err
	}
	if a != nil && a.Message != nil {
		m := a.Message
		refs := drive.References(m.Type, m.Body)
		blocked := len(refs) == 0 && slices.Contains([]string{"file", "image", "media", "audio", "folder"}, m.Type)
		for _, ref := range refs {
			if ref.Token != file {
				blocked = true
				break
			}
			ref.MessageID, ref.ChatKind = m.ID, string(a.Kind)
			if err := s.source.CheckShare(ctx, ref, target); err != nil {
				if errors.Is(err, drive.ErrUnavailable) {
					return err
				}
				blocked = true
				break
			}
		}
		if blocked {
			m.Body, e.Text, e.MessageID = "", "", ""
			return s.store.SuppressCommand(ctx, key, item)
		}
	}
	return ctx.Err()
}
