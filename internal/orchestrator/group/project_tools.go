package group

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
)

type projectCall struct {
	ToolCall
	Name string       `json:"tool"`
	Args pmtools.Args `json:"args"`
	Slot int          `json:"slot"`
}

var (
	errSlot          = errors.New("tool command changed")
	errToolReadStale = errors.New("project tool result changed")
)

const setPrimaryTool = "chat_set_primary"

func toolDigest(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (s *Service) projectTool(ctx context.Context, c projectCall) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.Roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		w, err := s.boundWork(ctx, tx, c.ToolCall)
		if err != nil {
			return err
		}
		if (w.Address.Mode != ProjectChat && w.Kind != "project_group_chat") || s.Projects == nil {
			return access.ErrForbidden
		}
		var bound bool
		if err = tx.QueryRow(ctx, "SELECT coalesce(bound_generation=$2 AND bound_turn=$3,false) FROM agent_group_work WHERE id=$1", w.Work, c.Generation, c.Turn).Scan(&bound); err != nil {
			return err
		}
		if !bound {
			return access.ErrForbidden
		}
		chatReads, err := s.verifyToolReads(ctx, tx, w.Work, w.Actor, w.Scope.ProjectID)
		if err != nil {
			return err
		}
		t, ok := pmtools.Lookup(c.Name)
		if !ok {
			return pmtools.ErrArguments
		}
		if err = t.Validate(c.Args); err != nil {
			return err
		}
		fp, err := toolDigest(c.Args)
		if err != nil {
			return err
		}
		if t.Write {
			if c.Slot < 1 || c.Slot > 128 {
				return pmtools.ErrArguments
			}
			allowed, err := s.Roles.CheckInOrganizationMutation(ctx, tx, w.Actor, t.Action, w.Scope.ProjectID)
			if err != nil {
				return err
			}
			if !allowed {
				return access.ErrForbidden
			}
			var name, hash string
			err = tx.QueryRow(ctx, "SELECT tool,fingerprint,receipt FROM agent_project_tool_calls WHERE work_id=$1 AND slot=$2", w.Work, c.Slot).Scan(&name, &hash, &result)
			if err == nil {
				if name != t.Name || hash != fp {
					return errSlot
				}
				return s.Projects.AuthorizeToolReceipt(ctx, tx, w.Actor, w.Scope.ProjectID, name, c.Args)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		} else if c.Slot != 0 {
			return pmtools.ErrArguments
		}
		val, err := s.Projects.RunTool(ctx, tx, w.Actor, w.Scope.ProjectID, t.Name, c.Args)
		if err != nil {
			return err
		}
		// A slow source callback may have outlived the lease while the row was locked.
		var live bool
		if err = tx.QueryRow(ctx, "SELECT deadline>clock_timestamp() AND lease_until>clock_timestamp() FROM agent_group_work WHERE id=$1", w.Work).Scan(&live); err != nil {
			return err
		}
		if !live {
			return access.ErrForbidden
		}
		result, err = json.Marshal(val)
		if err != nil {
			return err
		}
		if t.Write {
			if _, err = tx.Exec(ctx, "INSERT INTO agent_project_tool_calls(work_id,slot,tool,fingerprint,receipt) VALUES($1,$2,$3,$4,$5)", w.Work, c.Slot, t.Name, fp, result); err != nil {
				return err
			}
		}
		if t.Name == setPrimaryTool {
			for _, read := range chatReads {
				current, err := s.Projects.RefreshPrimaryChatRead(ctx, tx, w.Actor, w.Scope.ProjectID, read.Value)
				if err != nil {
					return err
				}
				digest, err := toolDigest(current)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE agent_project_tool_reads SET digest=$3
					WHERE work_id=$1 AND tool='chat_list' AND fingerprint=$2`, w.Work, read.Fingerprint, digest); err != nil {
					return err
				}
			}
		}
		if t.PrivateSource {
			digest, err := toolDigest(val)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "INSERT INTO agent_project_tool_reads(work_id,tool,fingerprint,args,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", w.Work, t.Name, fp, c.Args, digest)
			return err
		}
		return err
	})
	return result, err
}
func (s *Service) checkToolReads(ctx context.Context, tx pgx.Tx, workID string, actor identity.Principal, projectID string) error {
	_, err := s.verifyToolReads(ctx, tx, workID, actor, projectID)
	return err
}

// A committed primary change can be acknowledged without returning an old
// model answer or executing the write again.
func (s *Service) savedPrimaryReceipt(ctx context.Context, tx pgx.Tx, workID string, actor identity.Principal, projectID string) (bool, error) {
	if s.Projects == nil {
		return false, nil
	}
	var chatID json.RawMessage
	err := tx.QueryRow(ctx, `SELECT receipt->'chat_id' FROM agent_project_tool_calls
		WHERE work_id=$1 AND tool=$2 AND jsonb_typeof(receipt->'chat_id')='string' ORDER BY slot DESC LIMIT 1`,
		workID, setPrimaryTool).Scan(&chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	allowed, err := s.Roles.CheckInOrganizationMutation(ctx, tx, actor, "project.settings", projectID)
	if err != nil || !allowed {
		return false, err
	}
	err = s.Projects.AuthorizeToolReceipt(ctx, tx, actor, projectID, setPrimaryTool, pmtools.Args{"chat_id": chatID})
	if project.ToolStatus(err) == http.StatusForbidden {
		return false, nil
	}
	return err == nil, err
}

type checkedChatList struct {
	Fingerprint string
	Value       any
}

func (s *Service) verifyToolReads(ctx context.Context, tx pgx.Tx, workID string, actor identity.Principal, projectID string) ([]checkedChatList, error) {
	rows, err := tx.Query(ctx, "SELECT tool,args,digest,fingerprint FROM agent_project_tool_reads WHERE work_id=$1 ORDER BY tool,fingerprint", workID)
	if err != nil {
		return nil, err
	}
	type source struct {
		Name        string
		Args        pmtools.Args
		Digest      string
		Fingerprint string
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[source])
	if err != nil {
		return nil, err
	}
	var chatReads []checkedChatList
	for _, r := range refs {
		if s.Projects == nil {
			return nil, access.ErrForbidden
		}
		val, err := s.Projects.RunTool(ctx, tx, actor, projectID, r.Name, r.Args)
		if err != nil {
			switch project.ToolStatus(err) {
			case http.StatusForbidden:
				return nil, access.ErrForbidden
			case http.StatusConflict:
				return nil, errToolReadStale
			default:
				return nil, err
			}
		}
		digest, err := toolDigest(val)
		if err != nil {
			return nil, err
		}
		if digest != r.Digest {
			return nil, errToolReadStale
		}
		if r.Name == "chat_list" {
			chatReads = append(chatReads, checkedChatList{Fingerprint: r.Fingerprint, Value: val})
		}
	}
	return chatReads, nil
}
func (s *Service) registerProjectTools(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/tools/project/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !s.trustedRuntime(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pmtools.Schemas())
	})
	mux.HandleFunc("POST /internal/tools/project", func(w http.ResponseWriter, r *http.Request) {
		if !s.trustedRuntime(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		var c projectCall
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid", 400)
			return
		}
		result, err := s.projectTool(r.Context(), c)
		if err != nil {
			status := project.ToolStatus(err)
			if errors.Is(err, access.ErrForbidden) {
				status = 403
			}
			if errors.Is(err, errSlot) || errors.Is(err, errToolReadStale) {
				status = 409
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
	})
}
