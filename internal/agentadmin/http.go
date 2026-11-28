package agentadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type authFunc func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)

type settingsInput struct {
	Name    string   `json:"name"`
	Persona string   `json:"persona"`
	Enabled *bool    `json:"enabled"`
	Tools   []string `json:"tools"`
	Reason  string   `json:"reason"`
	Version *int64   `json:"expected_version"`
}

func (in *settingsInput) valid(role string) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Reason = strings.TrimSpace(in.Reason)
	in.Persona = strings.TrimSpace(in.Persona)
	if in.Name == "" || in.Reason == "" || strings.ContainsRune(in.Name+in.Reason+in.Persona, 0) || utf8.RuneCountInString(in.Name) > 48 || utf8.RuneCountInString(in.Persona) > 4000 || utf8.RuneCountInString(in.Reason) > 180 || in.Version == nil || *in.Version < 0 || in.Enabled == nil {
		return false
	}
	catalog := pmtools.RoleNames(role)
	if len(in.Tools) > len(catalog) {
		return false
	}
	seen := map[string]bool{}
	for _, t := range in.Tools {
		if seen[t] || !slices.Contains(catalog, t) {
			return false
		}
		seen[t] = true
	}
	return true
}

func (s Service) Register(mux *http.ServeMux, auth authFunc) {
	handle := func(w http.ResponseWriter, r *http.Request) {
		writing := r.Method == http.MethodPut
		policy := identity.ValidateSession
		if writing {
			policy = identity.RefreshSessionWithCSRF
		}
		actor, ok := auth(w, r, policy)
		if !ok {
			return
		}
		scope, role := r.PathValue("projectID"), r.PathValue("role")
		if scope != "" && !identity.ValidProjectID(scope) {
			http.Error(w, "invalid project", 400)
			return
		}
		if (role == "project-pm") != (scope != "") || names[role] == "" {
			http.NotFound(w, r)
			return
		}
		var in settingsInput
		if writing {
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 48<<10))
			d.DisallowUnknownFields()
			if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF || !in.valid(role) {
				http.Error(w, "invalid configuration", 400)
				return
			}
		}
		var c Config
		var edit bool
		var reason, author string
		op := func(tx pgx.Tx) error {
			permission := "system.configure"
			if scope != "" {
				permission = "project.settings"
			}
			var err error
			edit, err = s.Roles.CheckInOrganizationMutation(r.Context(), tx, actor, permission, scope)
			if err != nil {
				return err
			}
			if !edit {
				if writing || scope == "" {
					return access.ErrForbidden
				}
				read, err := s.Roles.CheckInOrganizationMutation(r.Context(), tx, actor, "project.read", scope)
				if err != nil {
					return err
				}
				if !read {
					return access.ErrForbidden
				}
			}
			if writing {
				c, err = save(r.Context(), tx, actor.TenantKey, actor.AppID, scope, actor.AccountID, in.Reason, Config{Role: role, Name: in.Name, Persona: in.Persona, Enabled: *in.Enabled, Version: *in.Version, Tools: in.Tools})
			} else {
				c, err = Load(r.Context(), tx, actor.TenantKey, actor.AppID, scope, role)
			}
			if err != nil || !edit || c.Version == 0 {
				return err
			}
			return tx.QueryRow(r.Context(), `SELECT reason,account_id FROM agent_configuration_changes
				WHERE (tenant_key,app_id,scope,role,version)=($1,$2,$3,$4,$5)`,
				actor.TenantKey, actor.AppID, scope, role, c.Version).Scan(&reason, &author)
		}
		var err error
		if writing {
			err = s.Roles.WithOrganizationMutation(r.Context(), op)
		} else {
			err = pgx.BeginFunc(r.Context(), s.DB, op)
		}
		if errors.Is(err, access.ErrForbidden) {
			http.Error(w, "forbidden", 403)
			return
		}
		if errors.Is(err, errConflict) {
			http.Error(w, "stale configuration", 409)
			return
		}
		if err != nil {
			http.Error(w, "configuration unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"config": c, "can_edit": edit, "available_tools": pmtools.RoleNames(role), "last_reason": reason, "last_actor": author})
	}
	for _, p := range []string{"/api/v1/agents/configurations/{role}", "/api/v1/projects/{projectID}/agents/configurations/{role}"} {
		mux.HandleFunc("GET "+p, handle)
		mux.HandleFunc("PUT "+p, handle)
	}
	mux.HandleFunc("GET /api/v1/agent-runs", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth(w, r, identity.ValidateSession)
		if !ok {
			return
		}
		items, err := s.runs(r.Context(), actor, r.URL.Query().Get("work_id"))
		if err != nil {
			http.Error(w, "runs unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
}

// BriefReceiptsSQL projects only durable receipt fields, never the private brief body.
// Both callers scope the outer work row (w) to its authorized viewer.
const BriefReceiptsSQL = `coalesce((SELECT jsonb_agg(jsonb_build_object('business_date',op.receipt->'business_date',
    'version',op.receipt->'version') ORDER BY op.slot) FROM agent_project_tool_calls op
    WHERE op.work_id=w.id AND op.tool='briefing_save' AND jsonb_typeof(op.receipt->'version')='number'
    AND op.receipt->>'business_date' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'), '[]'::jsonb)`

type BriefReceipt struct {
	Date    string `json:"business_date"`
	Version int64  `json:"version"`
}

type run struct {
	ID             string         `json:"id"`
	Project        string         `json:"project"`
	State          string         `json:"state"`
	Mode           string         `json:"mode"`
	Writes         int            `json:"writes"`
	Delivery       string         `json:"delivery_id"`
	Platform       string         `json:"platform_id"`
	Intent         string         `json:"intent"`
	Parent         string         `json:"parent_work"`
	Channel        string         `json:"channel"`
	Source         string         `json:"source_id"`
	Turn           int            `json:"turn"`
	Reason         string         `json:"reason"`
	WriteRequested bool           `json:"write_requested"`
	Briefs         []BriefReceipt `json:"briefs"`
	Tasks          []string       `json:"task_codes"`
	Failures       []writeFailure `json:"write_failures"`
}

type writeFailure struct {
	Slot   int    `json:"slot"`
	Tool   string `json:"tool"`
	Status int    `json:"status"`
}

func (s Service) runs(ctx context.Context, actor identity.Principal, id string) ([]run, error) {
	rows, err := s.DB.Query(ctx, `SELECT w.id,w.project_id,
        CASE WHEN w.deadline<=now() AND w.state IN ('route','running') THEN 'expired' ELSE w.state END,
        w.mode,(SELECT count(*) FROM agent_project_tool_calls t WHERE t.work_id=w.id),w.delivery_id::text,
        CASE WHEN w.channel='workbench' THEN '' ELSE coalesce(w.platform_message_id,'') END,
        w.intent,coalesce(w.parent_work,''),w.channel,w.message_id,coalesce(w.bound_turn,0),w.reason,w.write_requested,`+BriefReceiptsSQL+`,
        ARRAY(SELECT DISTINCT project_task_code(code.short_code,t.display_number)
          FROM agent_project_tool_calls op
          JOIN project_tasks t ON (t.tenant_key,t.app_id,t.project_id)=(w.tenant_key,w.app_id,w.project_id)
              AND t.id=op.receipt->>'id'
          JOIN project_short_codes code USING(tenant_key,app_id,project_id)
          WHERE op.work_id=w.id AND op.tool IN ('task_create','task_update','task_transition') ORDER BY 1),
        coalesce((SELECT jsonb_agg(jsonb_build_object('slot',f.slot,'tool',f.tool,'status',f.status) ORDER BY f.slot)
          FROM agent_project_tool_failures f WHERE f.work_id=w.id AND NOT EXISTS (
            SELECT FROM agent_project_tool_calls op WHERE (op.work_id,op.slot)=(f.work_id,f.slot))), '[]'::jsonb)
        FROM agent_group_work w JOIN accounts a ON (w.tenant_key,w.app_id,w.account_id)=(a.tenant_key,a.app_id,a.id)
        WHERE w.tenant_key=$1 AND w.app_id=$2 AND w.account_id=$3 AND a.active AND w.permission_version=a.permission_version
        AND (w.project_id='' OR EXISTS(SELECT FROM project_members m WHERE (m.tenant_key,m.app_id,m.project_id,m.account_id,m.generation)
        =(w.tenant_key,w.app_id,w.project_id,w.account_id,w.membership_generation) AND m.role!='revoked'))
        AND ($4='' OR w.id=$4 OR w.parent_work=$4) ORDER BY w.deadline DESC,w.id DESC LIMIT 100`, actor.TenantKey, actor.AppID, actor.AccountID, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[run])
}
