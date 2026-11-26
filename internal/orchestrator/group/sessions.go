package group

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type conversation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

var errTitleConflict = errors.New("conversation key reused")

func (s *Service) registerConversations(mux *http.ServeMux, authenticate func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
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
		var req struct {
			Title string `json:"title"`
		}
		key := r.Header.Get("Idempotency-Key")
		if post {
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			d.DisallowUnknownFields()
			if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF || !chatRequestID.MatchString(key) {
				http.Error(w, "invalid conversation", 400)
				return
			}
			req.Title = strings.TrimSpace(req.Title)
			if req.Title == "" || utf8.RuneCountInString(req.Title) > 80 || strings.ContainsRune(req.Title, 0) {
				http.Error(w, "invalid title", 400)
				return
			}
		}
		items := []conversation{{ID: "", Title: "项目对话"}}
		var created conversation
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			grant, err := s.Roles.ProjectGrant(r.Context(), tx, actor, project)
			if err != nil {
				return err
			}
			if !grant.Member {
				return access.ErrForbidden
			}
			if post {
				err = tx.QueryRow(r.Context(), `INSERT INTO agent_personal_conversations(tenant_key,app_id,account_id,project_id,membership_generation,title,request_key)
                    VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_key,app_id,account_id,project_id,membership_generation,request_key)
                    DO UPDATE SET title=excluded.title WHERE agent_personal_conversations.title=excluded.title RETURNING id,title`,
					s.Tenant, s.App, actor.AccountID, project, grant.MembershipGeneration, req.Title, key).Scan(&created.ID, &created.Title)
				if errors.Is(err, pgx.ErrNoRows) {
					return errTitleConflict
				}
				return err
			}
			rows, err := tx.Query(r.Context(), `SELECT id,title FROM agent_personal_conversations WHERE (tenant_key,app_id,account_id,project_id,membership_generation)
                =($1,$2,$3,$4,$5) ORDER BY created_at DESC,id DESC`, s.Tenant, s.App, actor.AccountID, project, grant.MembershipGeneration)
			if err != nil {
				return err
			}
			more, err := pgx.CollectRows(rows, pgx.RowToStructByPos[conversation])
			items = append(items, more...)
			return err
		})
		if errors.Is(err, access.ErrForbidden) {
			http.Error(w, "forbidden", 403)
			return
		}
		if errors.Is(err, errTitleConflict) {
			http.Error(w, "conversation key conflict", 409)
			return
		}
		if err != nil {
			http.Error(w, "conversation unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if post {
			_ = json.NewEncoder(w).Encode(created)
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		}
	}
	const path = "/api/v1/projects/{projectID}/bot/conversations"
	mux.HandleFunc("GET "+path, handle)
	mux.HandleFunc("POST "+path, handle)
}
