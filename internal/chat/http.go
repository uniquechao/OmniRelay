package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"golang.org/x/sync/errgroup"
)

const route = "/api/v1/feishu/chats"

var errHidden = errors.New("chat access could not be established")

type authorizedScope struct {
	Conversations []conversation
	Admin         bool
}

func (a authorizedScope) ids() []string {
	ids := make([]string, 0, len(a.Conversations))
	for _, item := range a.Conversations {
		ids = append(ids, item.ID)
	}
	return ids
}

type readHandler func(http.ResponseWriter, *http.Request, identity.Principal, authorizedScope)

func (s *Service) Register(mux *http.ServeMux,
	auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool),
) {
	protect := func(handler readHandler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			policy := identity.ValidateSession
			if r.Method == http.MethodPost {
				policy = identity.RefreshSessionWithCSRF
			}
			p, ok := auth(w, r, policy)
			if !ok {
				return
			}
			if p.TenantKey != s.tenant || p.AppID != s.app || p.AccountID == "" || p.OpenID == "" {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			chatID := r.PathValue("chatID")
			if chatID != "" && !validIdentifier(chatID) {
				writeError(w, http.StatusNotFound, "chat_access_lost")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			scope, err := s.authorizeProject(ctx, p, chatID, r.PathValue("projectID"))
			if err != nil {
				sourceError(w, err)
				return
			}
			if project := r.PathValue("projectID"); project != "" {
				// Recheck source membership and assignment versions before exposing the result.
				w = projectReadResponse{w, func() error {
					current, err := s.authorizeProject(ctx, p, chatID, project)
					if err != nil {
						return err
					}
					if !slices.Equal(scope.Conversations, current.Conversations) {
						return ErrAccessLost
					}
					return nil
				}}
			}
			handler(w, r.WithContext(ctx), p, scope)
		}
	}
	for _, prefix := range []string{route, "/api/v1/projects/{projectID}/chat-reader"} {
		mux.HandleFunc("GET "+prefix, protect(s.getConversations))
		mux.HandleFunc("GET "+prefix+"/search", protect(s.getMessages))
		mux.HandleFunc("GET "+prefix+"/sync", protect(s.getSync))
		mux.HandleFunc("GET "+prefix+"/{chatID}", protect(s.getConversations))
		mux.HandleFunc("GET "+prefix+"/{chatID}/members", protect(s.getMembers))
		mux.HandleFunc("GET "+prefix+"/{chatID}/messages", protect(s.getMessages))
		mux.HandleFunc("GET "+prefix+"/{chatID}/search", protect(s.getMessages))
		mux.HandleFunc("GET "+prefix+"/{chatID}/messages/{messageID}/context", protect(s.getContext))
		mux.HandleFunc("GET "+prefix+"/{chatID}/messages/{messageID}/resources/{kind}/{resource}", protect(s.getMessagePreview))
		mux.HandleFunc("GET "+prefix+"/{chatID}/sync", protect(s.getSync))
	}
	mux.HandleFunc("POST "+route+"/sync", protect(s.refresh))
	mux.HandleFunc("POST "+route+"/{chatID}/sync", protect(s.refresh))
	s.registerDelivery(mux, auth)
}

// CanReadConversation 供登录回跳复用与消息读取相同的会话范围。
func (s *Service) CanReadConversation(ctx context.Context, p identity.Principal, projectID, chatID string) (bool, error) {
	if !validIdentifier(chatID) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	_, err := s.authorizeProject(ctx, p, chatID, projectID)
	if errors.Is(err, ErrAccessLost) || errors.Is(err, errHidden) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) authorize(ctx context.Context, p identity.Principal, id string) (authorizedScope, error) {
	return s.authorizeProject(ctx, p, id, "")
}

func (s *Service) authorizeProject(ctx context.Context, p identity.Principal, id, project string) (authorizedScope, error) {
	if project != "" {
		allowed, err := s.check(ctx, p, "project.read", project)
		if err != nil {
			return authorizedScope{}, err
		}
		if !allowed {
			return authorizedScope{}, ErrAccessLost
		}
	}
	admin := false
	if project == "" {
		allowed, err := s.check(ctx, p, "chat.read_all", "")
		if err != nil {
			return authorizedScope{}, err
		}
		admin = allowed
	}
	candidates, err := s.listConversations(ctx, id, project)
	if err != nil {
		return authorizedScope{}, err
	}

	group, requests := errgroup.WithContext(ctx)
	group.SetLimit(6)
	allowed := make([]bool, len(candidates))
	for i, item := range candidates {
		group.Go(func() error {
			err := s.canRead(requests, p, admin, item)
			switch {
			case err == nil:
				allowed[i] = true
			case errors.Is(err, ErrAccessLost), errors.Is(err, errHidden):
				if id != "" {
					return err
				}
			default:
				return err
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return authorizedScope{}, err
	}
	items := make([]conversation, 0, len(candidates))
	for i, candidate := range candidates {
		if allowed[i] {
			items = append(items, candidate)
		}
	}
	if id != "" && len(items) == 0 {
		return authorizedScope{}, ErrAccessLost
	}
	if admin {
		stillAdmin, err := s.check(ctx, p, "chat.read_all", "")
		if err != nil {
			return authorizedScope{}, err
		}
		if !stillAdmin {
			return authorizedScope{}, ErrAccessLost
		}
	}
	return authorizedScope{Conversations: items, Admin: admin}, nil
}

func (s *Service) canRead(ctx context.Context, p identity.Principal, admin bool, c conversation) error {
	if c.Kind == orchestrator.ChatPrivate {
		if !admin && c.Peer != p.OpenID {
			return errHidden
		}
	} else {
		// The live members endpoint checks the bot's participation and project
		// or system roles cannot stand in for the requesting user's membership.
		if !admin {
			member, err := s.hasMember(ctx, c.ID, p.OpenID)
			if err != nil && !errors.Is(err, ErrAccessLost) && !errors.Is(err, ErrPartial) {
				return err
			}
			if !member {
				return errHidden
			}
		}
		if _, err := s.source.Group(ctx, c.ID); err != nil {
			return err
		}
	}
	// Group metadata and private-peer bindings alone do not verify that the
	// application still has a scope to read this conversation's messages.
	_, err := s.source.History(ctx, HistoryQuery{ChatID: c.ID, Limit: 1})
	return err
}

func (s *Service) hasMember(ctx context.Context, id, openID string) (bool, error) {
	seen := make(map[string]bool)
	for cursor := ""; ; {
		page, err := s.source.Members(ctx, id, cursor)
		if err != nil && !errors.Is(err, ErrPartial) {
			return false, err
		}
		if slices.ContainsFunc(page.Items, func(member Member) bool { return member.OpenID == openID }) {
			return true, nil
		}
		if err != nil || page.Next == "" {
			return false, err
		}
		if seen[page.Next] {
			return false, ErrUnavailable
		}
		seen[page.Next], cursor = true, page.Next
	}
}

func (s *Service) getMembers(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	ctx := r.Context()
	c := scope.Conversations[0]
	limit, offset, ok := offsetPage(r)
	if !ok {
		writeError(w, 400, "invalid_chat_query")
		return
	}
	// 只有具备当前群发送能力的调用者可以选择真实提及对象。
	mentions := r.URL.Query().Get("purpose") == "mention"
	if mentions {
		if !s.deliveryEnabled || s.delivery == nil {
			deliveryHTTPError(w, errDeliveryDisabled)
			return
		}
		if _, err := s.deliveryTarget(ctx, p, c.ID, nil, true); err != nil {
			deliveryHTTPError(w, err)
			return
		}
	}
	state := "complete"
	var refs []directory.ResourcePersonRef
	if c.Kind == orchestrator.ChatPrivate {
		bot, err := s.source.Bot(ctx)
		if err != nil {
			sourceError(w, err)
			return
		}
		refs = append(refs, directory.ResourcePersonRef{OpenID: c.Peer, Name: c.Name},
			directory.ResourcePersonRef{OpenID: bot.OpenID, Name: bot.Name})
	} else {
		seenPages := make(map[string]bool)
		seenMembers := make(map[string]bool)
		for cursor := ""; ; {
			page, err := s.source.Members(ctx, c.ID, cursor)
			if err != nil && !errors.Is(err, ErrPartial) {
				sourceError(w, err)
				return
			}
			for _, member := range page.Items {
				if !seenMembers[member.OpenID] {
					refs = append(refs, directory.ResourcePersonRef{OpenID: member.OpenID, Name: member.Name, GroupMember: true})
					seenMembers[member.OpenID] = true
				}
			}
			if errors.Is(err, ErrPartial) {
				state = "partial"
				break
			}
			if page.Next == "" {
				break
			}
			if seenPages[page.Next] {
				sourceError(w, ErrUnavailable)
				return
			}
			seenPages[page.Next], cursor = true, page.Next
		}
		if !scope.Admin && !seenMembers[p.OpenID] {
			sourceError(w, ErrAccessLost)
			return
		}
	}
	count := len(refs)
	first := min(offset, count)
	items, err := s.projectPeople(ctx, p, c.ID, refs[first:min(first+limit, count)])
	if err != nil {
		sourceError(w, err)
		return
	}
	if mentions {
		type choice struct {
			Name   string `json:"name"`
			OpenID string `json:"mention_open_id"`
		}
		choices := make([]choice, 0, len(items))
		for i, person := range items {
			choices = append(choices, choice{person.Name, refs[first+i].OpenID})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": choices, "total": count, "state": state})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": count, "state": state})
}

func (s *Service) projectPeople(ctx context.Context, p identity.Principal, scopeID string,
	refs []directory.ResourcePersonRef,
) ([]directory.PersonProjection, error) {
	return s.people.ReadResourcePeople(ctx, p, scopeID,
		func(context.Context, identity.Principal, string) ([]directory.ResourcePersonRef, bool, error) {
			return refs, true, nil
		}, s.check)
}

func offsetPage(r *http.Request) (int, int, bool) {
	limit, ok := requestedLimit(r, 50, 200)
	if !ok {
		return 0, 0, false
	}
	offset := 0
	if r.URL.Query().Has("offset") {
		var err error
		offset, err = strconv.Atoi(r.URL.Query().Get("offset"))
		if err != nil || offset < 0 || offset > 1_000_000 {
			return 0, 0, false
		}
	}
	return limit, offset, true
}

func requestedLimit(r *http.Request, defaultLimit, maxLimit int) (int, bool) {
	if !r.URL.Query().Has("limit") {
		return defaultLimit, true
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	return n, err == nil && n > 0 && n <= maxLimit
}

func searchQuery(r *http.Request) (string, bool) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	return q, utf8.ValidString(q) && utf8.RuneCountInString(q) <= 200 && !strings.ContainsRune(q, 0)
}

func validIdentifier(id string) bool {
	if id == "" || len(id) > 160 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func sourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAccessLost) || errors.Is(err, errHidden) {
		writeError(w, http.StatusNotFound, "chat_access_lost")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "chat_unavailable")
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

type projectReadResponse struct {
	http.ResponseWriter
	validate func() error
}

func validateProjectRead(w http.ResponseWriter) bool {
	if scoped, ok := w.(projectReadResponse); ok {
		if err := scoped.validate(); err != nil {
			sourceError(scoped.ResponseWriter, err)
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	if status == http.StatusOK && !validateProjectRead(w) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Confirm current local account and source configuration in the same snapshot
// used for message results and totals.
func (s *Service) readTx(ctx context.Context, p identity.Principal) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a
		JOIN feishu_bot_configs b ON (b.tenant_key,b.app_id)=(a.tenant_key,a.app_id)
		WHERE a.id=$1 AND a.tenant_key=$2 AND a.app_id=$3 AND a.active AND a.open_id=$4
			AND b.enabled AND b.credentials_generation=$5)`, p.AccountID, s.tenant, s.app, p.OpenID, s.generation).Scan(&active)
	if err != nil || !active {
		_ = tx.Rollback(ctx)
		if err == nil {
			err = ErrAccessLost
		}
		return nil, err
	}
	return tx, nil
}
