package group

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// RegisterNotices exposes the application side of scheduling. Only the private
// owner listener can bind the native timer or report its occurrence.
func (s *Service) RegisterNotices(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool),
	channel string, version func(context.Context) (int64, error)) {
	route := "/api/v1/projects/{projectID}/agent-notices"
	handle := func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth(w, r, identity.RefreshSessionWithCSRF)
		if !ok {
			return
		}
		if actor.TenantKey != s.Tenant || actor.AppID != s.App {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		project := r.PathValue("projectID")
		if !identity.ValidProjectID(project) {
			http.Error(w, "invalid project", 400)
			return
		}
		var err error
		if r.Method == http.MethodDelete {
			err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
				allowed, err := s.Roles.CheckInOrganizationMutation(r.Context(), tx, actor, "project.settings", project)
				if err != nil {
					return err
				}
				if !allowed {
					return access.ErrForbidden
				}
				tag, err := tx.Exec(r.Context(), `UPDATE agent_notice_plans p SET enabled=false FROM agent_instances i
              WHERE p.agent_id=i.agent_id AND p.id::text=$1 AND i.tenant_key=$2 AND i.app_id=$3 AND i.project_id=$4`,
					r.PathValue("planID"), s.Tenant, s.App, project)
				if err == nil && tag.RowsAffected() == 0 {
					return access.ErrForbidden
				}
				return err
			})
			if err == nil {
				w.WriteHeader(204)
				return
			}
		} else {
			var body struct {
				At      time.Time      `json:"at"`
				Targets []NoticeTarget `json:"targets"`
			}
			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
			d.DisallowUnknownFields()
			if key == "" || len(key) > 128 || d.Decode(&body) != nil || d.Decode(&struct{}{}) != io.EOF || body.At.IsZero() || body.At.Nanosecond() != 0 || len(body.Targets) == 0 {
				http.Error(w, "invalid notice", 400)
				return
			}
			var config int64
			config, err = version(r.Context())
			if err == nil {
				var id string
				id, err = s.CreateNoticePlan(r.Context(), actor, NoticeSpec{Key: key, At: body.At, Targets: body.Targets, Project: project, Channel: channel, Config: config})
				if err == nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(202)
					_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
					return
				}
			}
		}
		switch {
		case errors.Is(err, access.ErrForbidden):
			http.Error(w, "forbidden", 403)
		case errors.Is(err, errNoticeConflict):
			http.Error(w, "request key conflict", 409)
		default:
			http.Error(w, "notice unavailable", 503)
		}
	}
	mux.HandleFunc("POST "+route, handle)
	mux.HandleFunc("DELETE "+route+"/{planID}", handle)
}
