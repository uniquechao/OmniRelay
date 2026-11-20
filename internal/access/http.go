package access

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
	maxRequestBytes = 4096
)

type authenticatedHandler func(http.ResponseWriter, *http.Request, identity.Principal)

// Register exposes the administrative API using the supplied session and CSRF validator.
func (s *Service) Register(mux *http.ServeMux, authenticate func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	register := func(pattern string, policy identity.SessionPolicy, handler authenticatedHandler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if principal, ok := authenticate(w, r, policy); ok {
				handler(w, r, principal)
			}
		})
	}
	// The directory view also reads accounts and grants during snapshot refresh.
	register("GET /api/v1/system/accounts", identity.ValidateSession, s.handleAccounts)
	register("POST /api/v1/system/accounts", identity.RefreshSessionWithCSRF, s.handlePreauthorizeAccount)
	register("PATCH /api/v1/system/accounts/{accountID}", identity.RefreshSessionWithCSRF, s.handleAccountStatus)
	register("GET /api/v1/system/roles", identity.RefreshSession, s.handleRoles)
	register("GET /api/v1/system/role-grants", identity.ValidateSession, s.handleRoleGrants)
	register("POST /api/v1/system/role-grants", identity.RefreshSessionWithCSRF, s.handleCreateGrant)
	register("DELETE /api/v1/system/role-grants/{grantID}", identity.RefreshSessionWithCSRF, s.handleRevokeGrant)
	register("GET /api/v1/system/audit-events", identity.RefreshSession, s.handleAuditEvents)
}

func (s *Service) handleRoles(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	if err := s.require(r.Context(), s.pool, actor, "role.read", ""); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []roleDefinition `json:"items"`
	}{Items: listRoleDefinitions()})
}

func (s *Service) handleAccounts(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	if err := s.require(r.Context(), s.pool, actor, "account.read", ""); err != nil {
		writeError(w, err)
		return
	}
	after, limit, err := readPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var active *bool
	if filter := r.URL.Query().Get("active"); filter != "" {
		if filter != "true" && filter != "false" {
			writeError(w, errInvalidInput)
			return
		}
		value := filter == "true"
		active = &value
	}
	accounts, err := s.listAccounts(r.Context(), after, limit+1, active)
	if err != nil {
		writeError(w, err)
		return
	}
	writePage(w, accounts, limit, func(account Account) string { return account.ID })
}

func (s *Service) handlePreauthorizeAccount(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	var body struct {
		OpenID string `json:"openId"`
	}
	if err := readBody(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	if !validIdentifier(body.OpenID) {
		writeError(w, errInvalidInput)
		return
	}
	account, err := s.preauthorizeAccount(r.Context(), actor, body.OpenID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (s *Service) handleAccountStatus(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	accountID := r.PathValue("accountID")
	if !validIdentifier(accountID) {
		writeError(w, errInvalidInput)
		return
	}
	var body struct {
		Active *bool `json:"active"`
	}
	if err := readBody(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Active == nil {
		writeError(w, errInvalidInput)
		return
	}
	account, err := s.setAccountActive(r.Context(), actor, accountID, *body.Active)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Service) handleRoleGrants(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	projectID, accountID := r.URL.Query().Get("projectId"), r.URL.Query().Get("accountId")
	if (projectID != "" && !validIdentifier(projectID)) || (accountID != "" && !validIdentifier(accountID)) {
		writeError(w, errInvalidInput)
		return
	}
	action := "role.read"
	if projectID != "" {
		action = "project.delegate"
	}
	if err := s.require(r.Context(), s.pool, actor, action, projectID); err != nil {
		writeError(w, err)
		return
	}
	after, limit, err := readPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	grants, err := s.listGrants(r.Context(), projectID, accountID, after, limit+1)
	if err != nil {
		writeError(w, err)
		return
	}
	writePage(w, grants, limit, func(grant RoleGrant) string { return grant.ID })
}

func (s *Service) handleCreateGrant(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	var body grantRequest
	if err := readBody(w, r, &body); err != nil {
		writeError(w, err)
		return
	}
	grant, err := s.grantRole(r.Context(), actor, body)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Service) handleRevokeGrant(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	grantID := r.PathValue("grantID")
	if !validIdentifier(grantID) {
		writeError(w, errInvalidInput)
		return
	}
	if err := s.revokeRole(r.Context(), actor, grantID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleAuditEvents(w http.ResponseWriter, r *http.Request, actor identity.Principal) {
	if err := s.require(r.Context(), s.pool, actor, "audit.read", ""); err != nil {
		writeError(w, err)
		return
	}
	query, err := parseAuditQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, limit, err := readPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if query.extended {
		page, err := s.queryAudit(r.Context(), query, cursor, limit)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	var before *int64
	if cursor != "" {
		id, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || id < 1 {
			writeError(w, errInvalidInput)
			return
		}
		before = &id
	}
	events, err := s.listAudit(r.Context(), before, limit+1)
	if err != nil {
		writeError(w, err)
		return
	}
	writePage(w, events, limit, func(event AuditEvent) string { return strconv.FormatInt(event.ID, 10) })
}

func readPage(r *http.Request) (string, int, error) {
	query := r.URL.Query()
	cursor := query.Get("cursor")
	if cursor != "" && !validIdentifier(cursor) {
		return "", 0, errInvalidInput
	}
	limit := defaultPageSize
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxPageSize {
			return "", 0, errInvalidInput
		}
		limit = value
	}
	return cursor, limit, nil
}

func readBody(w http.ResponseWriter, r *http.Request, dest any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(dest)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			return nil
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return err
	}
	return errInvalidInput
}

func writePage[T any](w http.ResponseWriter, items []T, limit int, cursor func(T) string) {
	var nextCursor string
	if len(items) > limit {
		items = items[:limit]
		nextCursor = cursor(items[limit-1])
	}
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []T    `json:"items"`
		NextCursor string `json:"nextCursor,omitempty"`
	}{Items: items, NextCursor: nextCursor})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		status, message = http.StatusRequestEntityTooLarge, "request body too large"
	case errors.Is(err, errInvalidInput):
		status, message = http.StatusBadRequest, "invalid request"
	case errors.Is(err, ErrForbidden):
		status, message = http.StatusForbidden, "permission denied"
	case errors.Is(err, errNotFound):
		status, message = http.StatusNotFound, "resource not found"
	case errors.Is(err, errAccountResigned):
		status, message = http.StatusConflict, "account_resigned"
	case errors.Is(err, errLastAdmin), errors.Is(err, errDuplicateGrant), errors.Is(err, errDuplicateAccount):
		status, message = http.StatusConflict, err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}
