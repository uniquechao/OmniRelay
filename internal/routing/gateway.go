// Package routing resolves an authenticated request to a logical Agent.
package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var errInvalidIntent = errors.New("invalid agent intent or project")

type Gateway struct {
	Check func(context.Context, identity.Principal, string, string) (bool, error)
}

type Target struct {
	AgentID string `json:"agentId"`
	Role    string `json:"role"`
	SpaceID string `json:"spaceId"`
}

// Resolve checks admission only. Tool execution and publication recheck access.
// Project is a resource selector, never proof of the caller's membership.
func (g Gateway) Resolve(ctx context.Context, actor identity.Principal, intent, project string) (Target, error) {
	if actor.TenantKey == "" || actor.AppID == "" || actor.AccountID == "" {
		return Target{}, access.ErrForbidden
	}
	if len(project) > 256 || strings.TrimSpace(project) != project || strings.ContainsRune(project, 0) {
		return Target{}, errInvalidIntent
	}
	var role, space, action string
	switch intent {
	case "admin":
		role, space, action = "management-pm", "global:management", "agent.management.enter"
	case "knowledge", "hours":
		role, space, action = "query", "global:service", "agent.query.enter"
	case "project":
		role, space, action = "project-pm", "project:"+project, "project.read"
	default:
		return Target{}, errInvalidIntent
	}
	if intent == "hours" || intent == "project" {
		if project == "" {
			return Target{}, errInvalidIntent
		}
		// Hours uses the global assistant while its target project requires membership.
		action = "project.read"
	} else if project != "" {
		return Target{}, errInvalidIntent
	}
	ok, err := g.Check(ctx, actor, action, project)
	if err != nil {
		return Target{}, err
	}
	if !ok {
		return Target{}, access.ErrForbidden
	}
	// Length-delimited JSON strings keep all namespace components unambiguous.
	key, err := json.Marshal([]string{actor.TenantKey, actor.AppID, role, space})
	if err != nil {
		return Target{}, err
	}
	sum := sha256.Sum256(key)
	return Target{AgentID: hex.EncodeToString(sum[:]), Role: role, SpaceID: space}, nil
}

// Register exposes a read-only routing preflight; it never dispatches a turn.
func (g Gateway) Register(mux *http.ServeMux, authenticate func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	mux.HandleFunc("GET /api/v1/agents/resolve", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := authenticate(w, r, identity.ValidateSession)
		if !ok {
			return
		}
		q := r.URL.Query()
		target, err := g.Resolve(r.Context(), actor, q.Get("intent"), q.Get("projectId"))
		switch {
		case errors.Is(err, errInvalidIntent):
			http.Error(w, "invalid or incomplete route", http.StatusBadRequest)
		case errors.Is(err, access.ErrForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case err != nil:
			http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(target)
		}
	})
}
