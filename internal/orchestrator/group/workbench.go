package group

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agentadmin"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var chatRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,96}$`)
var errChatBusy = errors.New("another message is pending")

func validQuestion(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 2000 && !strings.ContainsRune(s, 0)
}

type chatMessage struct {
	Briefs       []agentadmin.BriefReceipt `json:"briefs,omitempty"`
	ID           string                    `json:"id"`
	Question     string                    `json:"question"`
	Answer       string                    `json:"answer,omitempty"`
	State        string                    `json:"state"`
	PrimarySaved bool                      `json:"primary_saved,omitempty"`
	AgentName    string                    `json:"agent_name"`
}

func (s *Service) RegisterWorkbench(mux *http.ServeMux, authenticate func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	s.registerConversations(mux, authenticate)
	ch := Channel{Resolve: func(ctx context.Context, tx pgx.Tx, e Envelope) (Assignment, error) {
		if e.Mode != ProjectChat || e.Project != e.Chat || e.Channel != "workbench" || e.Project == "" {
			return Assignment{}, access.ErrForbidden
		}
		if e.Thread != "" {
			var exists bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM agent_personal_conversations c JOIN project_members m
				ON (c.tenant_key,c.app_id,c.account_id,c.project_id,c.membership_generation)=(m.tenant_key,m.app_id,m.account_id,m.project_id,m.generation)
				WHERE (c.tenant_key,c.app_id,c.account_id,c.project_id,c.id)=($1,$2,$3,$4,$5) AND m.role!='revoked')`, e.Tenant, e.App, e.Account, e.Project, e.Thread).Scan(&exists)
			if err != nil {
				return Assignment{}, err
			}
			if !exists {
				return Assignment{}, access.ErrForbidden
			}
		}
		return Assignment{Project: e.Project, Bot: "workbench"}, nil
	}}
	s.Channels["workbench"] = ch
	handle := func(w http.ResponseWriter, r *http.Request) {
		post := r.Method == http.MethodPost
		policy := identity.ValidateSession
		if post {
			policy = identity.RefreshSessionWithCSRF
		}
		actor, ok := authenticate(w, r, policy)
		if !ok {
			return
		}
		project := r.PathValue("projectID")
		if !identity.ValidProjectID(project) || actor.TenantKey != s.Tenant || actor.AppID != s.App {
			http.Error(w, "forbidden", 403)
			return
		}
		thread := r.URL.Query().Get("conversation")
		var body struct {
			Text   string `json:"text"`
			Intent string `json:"intent"`
		}
		key := r.Header.Get("Idempotency-Key")
		if post {
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<10))
			d.DisallowUnknownFields()
			if d.Decode(&body) != nil || d.Decode(&struct{}{}) != io.EOF || !validQuestion(body.Text) || !chatRequestID.MatchString(key) || (body.Intent != "" && body.Intent != "project" && body.Intent != "knowledge" && body.Intent != "admin") {
				http.Error(w, "invalid message", 400)
				return
			}
		}
		ctx := r.Context()
		items := []chatMessage{}
		var id string
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			grant, err := s.Roles.ProjectGrant(ctx, tx, actor, project)
			if err != nil {
				return err
			}
			if !grant.Member {
				return access.ErrForbidden
			}
			if thread != "" {
				_, err = ch.Resolve(ctx, tx, Envelope{Tenant: s.Tenant, App: s.App, Account: actor.AccountID, Project: project, Chat: project, Thread: thread, Mode: ProjectChat, Channel: "workbench"})
				if err != nil {
					return err
				}
			}
			if post {
				lock, _ := json.Marshal([]string{s.Tenant, s.App, actor.AccountID, project, thread})
				if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))", string(lock)); err != nil {
					return err
				}
				var busy bool
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM agent_group_work WHERE tenant_key=$1 AND app_id=$2 AND account_id=$3 AND project_id=$4 AND mode='project_chat' AND thread_id=$6 AND message_id!=$5 AND deadline>now() AND state IN ('route','running','ready','sending')) AND NOT EXISTS(SELECT FROM agent_group_work WHERE tenant_key=$1 AND app_id=$2 AND channel='workbench' AND message_id=$5)`, s.Tenant, s.App, actor.AccountID, project, key, thread).Scan(&busy)
				if err != nil {
					return err
				}
				if busy {
					return errChatBusy
				}
				id, err = Accept(ctx, tx, ch, Envelope{Tenant: s.Tenant, App: s.App, Channel: "workbench", Account: actor.AccountID, Chat: project, Thread: thread, Project: project, Mode: ProjectChat, Message: key, Intent: body.Intent, Text: strings.TrimSpace(body.Text), Received: time.Now()})
				return err
			}
			rows, err := tx.Query(ctx, `SELECT w.id,coalesce(w.question,w.group_question),CASE WHEN w.group_answer IS NOT NULL AND w.state IN ('ready','sending','sent','unknown','failed') THEN w.group_answer WHEN w.state='sent' THEN w.response_text ELSE '' END,
				CASE WHEN w.deadline<=now() AND w.state IN ('route','running') THEN 'expired'
				     WHEN w.state='suppressed' AND w.reason='tool_source_changed' THEN 'stale' ELSE w.state END,
				coalesce(w.config_snapshot->>'name',CASE w.intent WHEN 'knowledge' THEN '知识问答 Agent' WHEN 'admin' THEN '全局管理 Agent' ELSE '项目 PM' END),`+agentadmin.BriefReceiptsSQL+`
				FROM agent_group_work w JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(w.tenant_key,w.app_id,w.account_id)
				JOIN project_members m ON (m.tenant_key,m.app_id,m.account_id,m.project_id)=(w.tenant_key,w.app_id,w.account_id,w.project_id)
				WHERE m.generation=w.membership_generation AND m.role!='revoked' AND w.tenant_key=$1 AND w.app_id=$2 AND w.account_id=$3 AND w.project_id=$4 AND ((w.mode='project_chat' AND w.thread_id=$6) OR ($6='' AND w.group_question IS NOT NULL))
				AND w.membership_generation=$5 AND a.active AND w.permission_version=a.permission_version ORDER BY coalesce(w.asked_at,w.group_asked_at) DESC,w.id DESC LIMIT 50`, s.Tenant, s.App, actor.AccountID, project, grant.MembershipGeneration, thread)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var m chatMessage
				if err := rows.Scan(&m.ID, &m.Question, &m.Answer, &m.State, &m.AgentName, &m.Briefs); err != nil {
					return err
				}
				items = append(items, m)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			rows.Close()
			for i := range items {
				message := &items[i]
				if message.Answer != "" {
					switch err = s.checkToolReads(ctx, tx, message.ID, actor, project); {
					case errors.Is(err, errToolReadStale):
						message.Answer, message.State = "", "stale"
					case errors.Is(err, access.ErrForbidden):
						message.Answer, message.State = "", "suppressed"
					case err != nil:
						return err
					}
				}
				switch message.State {
				case "stale", "suppressed", "expired":
					message.PrimarySaved, err = s.savedPrimaryReceipt(ctx, tx, message.ID, actor, project)
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
		switch {
		case errors.Is(err, access.ErrForbidden):
			http.Error(w, "forbidden", 403)
		case errors.Is(err, errChatBusy):
			http.Error(w, "message pending", 409)
		case err != nil:
			http.Error(w, "conversation unavailable", 503)
		default:
			w.Header().Set("Content-Type", "application/json")
			if post {
				w.WriteHeader(202)
				_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
				return
			}
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		}
	}
	const path = "/api/v1/projects/{projectID}/bot/messages"
	mux.HandleFunc("GET "+path, handle)
	mux.HandleFunc("POST "+path, handle)
}
