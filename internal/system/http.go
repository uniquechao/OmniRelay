package system

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

const basePath = "/api/v1/system/feishu-bot"

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type authenticate func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)
type handler func(http.ResponseWriter, *http.Request, identity.Principal)

func (s *Service) Register(mux *http.ServeMux, auth authenticate, permissions Permissions) {
	handle := func(pattern, action string, policy identity.SessionPolicy, next handler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := auth(w, r, policy)
			if !ok {
				return
			}
			allowed, err := permissions.Check(r.Context(), actor, action, "")
			if err != nil {
				failure(w, err)
				return
			}
			if !allowed {
				failure(w, access.ErrForbidden)
				return
			}
			next(w, r, actor)
		})
	}
	handle("GET "+basePath, "system.read", identity.ValidateSession, s.getStatus)
	handle("PATCH "+basePath, "system.configure", identity.RefreshSessionWithCSRF, s.configure)
	handle("POST "+basePath+"/checks", "system.operate", identity.RefreshSessionWithCSRF, s.submit)
	handle("GET "+basePath+"/checks/{id}", "system.read", identity.ValidateSession, s.getCheck)
	handle("GET /api/v1/system/connections", "system.read", identity.ValidateSession, s.getConnections)
}

func (s *Service) getConnections(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	type connectionSetting struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	state := "not_configured"
	if s.RuntimeReadiness != nil {
		state = s.RuntimeReadiness(r.Context())
	}
	respond(w, http.StatusOK, struct {
		Items []connectionSetting `json:"items"`
	}{Items: []connectionSetting{
		{ID: "omnirelay", State: "local"},
		{ID: "deepseek_harness", State: state},
	}})
}

func (s *Service) getStatus(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	status, err := s.store.Status(r.Context())
	writeStatus(w, status, err)
}

func (s *Service) configure(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	match := r.Header.Values("If-Match")
	if len(match) == 0 {
		problem(w, http.StatusPreconditionRequired, "config_version_required")
		return
	}
	if len(match) != 1 {
		failure(w, ErrVersionConflict)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') || !decoder.More() {
		problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	key, err := decoder.Token()
	var enabled *bool
	if err != nil || key != "enabled" || decoder.Decode(&enabled) != nil || enabled == nil || decoder.More() {
		problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') || decoder.Decode(new(json.RawMessage)) != io.EOF {
		problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	status, err := s.store.Configure(r.Context(), actor, match[0], *enabled)
	writeStatus(w, status, err)
}

func (s *Service) submit(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	var body map[string]json.RawMessage
	if !readObject(w, r, &body) || body == nil || len(body) != 0 {
		problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	key := r.Header.Values("Idempotency-Key")
	if len(key) != 1 || !keyPattern.MatchString(key[0]) {
		problem(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	task, err := s.store.Submit(r.Context(), actor, key[0])
	if err != nil {
		failure(w, err)
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	respond(w, http.StatusAccepted, task)
}

func (s *Service) getCheck(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	task, err := s.store.Check(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	results := make([]Result, 0, len(task.Results))
	for _, item := range task.Results {
		if item.Name == tenantTokenCheck || item.Name == botInfoCheck {
			results = append(results, item)
		}
	}
	task.Results = results
	respond(w, http.StatusOK, task)
}

func readObject(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if decoder.Decode(target) != nil {
		return false
	}
	return decoder.Decode(new(json.RawMessage)) == io.EOF
}

func writeStatus(w http.ResponseWriter, status Status, err error) {
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("ETag", status.ETag())
	respond(w, http.StatusOK, status)
}

func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrForbidden):
		problem(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrVersionConflict):
		problem(w, http.StatusPreconditionFailed, "config_version_conflict")
	case errors.Is(err, ErrKeyConflict):
		problem(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, ErrCheckNotFound):
		problem(w, http.StatusNotFound, "check_not_found")
	default:
		problem(w, http.StatusServiceUnavailable, "service_unavailable")
	}
}

func problem(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
