package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

const visibleMessageRows = `m.tenant_key=$1 AND m.app_id=$2
	AND (m.thread_id='' OR m.visible_in_chat OR NOT EXISTS (
		SELECT 1 FROM feishu_chat_sync t WHERE (t.tenant_key,t.app_id,t.chat_id,t.thread_id)=
			(m.tenant_key,m.app_id,m.chat_id,m.thread_id) AND t.source_blocked
	))`

const visibleMessages = visibleMessageRows + ` AND m.chat_id=ANY($3::text[])`

type conversationItem struct {
	conversation
	ProjectAssignment *ProjectAssignment          `json:"project_assignment,omitempty"`
	LastMessageAt     *time.Time                  `json:"last_message_at"`
	Person            *directory.PersonProjection `json:"person,omitempty"`
}

func (s *Service) getConversations(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	q, ok := searchQuery(r)
	limit, offset, validPage := offsetPage(r)
	kind := r.URL.Query().Get("kind")
	if !ok || !validPage || (kind != "" && kind != "group" && kind != "p2p") {
		writeError(w, 400, "invalid_chat_query")
		return
	}
	ctx := r.Context()
	tx, err := s.readTx(ctx, p)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	const matched = `WITH latest AS (
		SELECT chat_id,max(sent_at) AS last_message_at,
			bool_or($5<>'' AND (strpos(lower(search_text),lower($5))>0 OR strpos(lower(sender_name),lower($5))>0)) AS matched_message
		FROM feishu_chat_messages m WHERE ` + visibleMessages + ` AND NOT deleted GROUP BY chat_id
	), matched AS (
		SELECT c.chat_id,c.kind,c.name,c.peer_open_id,l.last_message_at,
			coalesce(a.project_id,''),coalesce(a.primary_group,false),coalesce(a.version,0)
		FROM feishu_chat_conversations c LEFT JOIN latest l USING (chat_id)
		LEFT JOIN project_chat_assignments a USING (tenant_key,app_id,chat_id)
		WHERE c.tenant_key=$1 AND c.app_id=$2 AND c.chat_id=ANY($3::text[])
			AND ($4='' OR c.kind=$4)
			AND ($5='' OR strpos(lower(c.name),lower($5))>0 OR l.matched_message)
	)`
	args := []any{s.tenant, s.app, scope.ids(), kind, q}
	var total int
	if err := tx.QueryRow(ctx, matched+` SELECT count(*) FROM matched`, args...).Scan(&total); err != nil {
		sourceError(w, err)
		return
	}
	rows, err := tx.Query(ctx, matched+` SELECT * FROM matched
		ORDER BY last_message_at DESC NULLS LAST,chat_id LIMIT $6 OFFSET $7`, append(args, limit, offset)...)
	if err != nil {
		sourceError(w, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversationItem, error) {
		var item conversationItem
		var assignment ProjectAssignment
		err := row.Scan(&item.ID, &item.Kind, &item.Name, &item.Peer, &item.LastMessageAt,
			&assignment.ProjectID, &assignment.Primary, &assignment.Version)
		if item.Kind == orchestrator.ChatGroup {
			item.ProjectAssignment = &assignment
		}
		if item.LastMessageAt != nil {
			t := item.LastMessageAt.UTC()
			item.LastMessageAt = &t
		}
		return item, err
	})
	if err != nil {
		sourceError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		sourceError(w, err)
		return
	}
	var refs []directory.ResourcePersonRef
	var indexes []int
	for i, item := range items {
		if item.Kind == orchestrator.ChatPrivate {
			refs = append(refs, directory.ResourcePersonRef{OpenID: item.Peer, Name: item.Name})
			indexes = append(indexes, i)
		}
	}
	if len(refs) != 0 {
		people, err := s.projectPeople(ctx, p, chatScopeID(r), refs)
		if err != nil {
			sourceError(w, err)
			return
		}
		for i, index := range indexes {
			items[index].Person = &people[i]
		}
	}
	projectReadable := make(map[string]bool)
	for i := range items {
		assignment := items[i].ProjectAssignment
		if assignment == nil || assignment.ProjectID == "" {
			continue
		}
		allowed, checked := projectReadable[assignment.ProjectID]
		if !checked {
			visible, authErr := s.check(ctx, p, "project.read", assignment.ProjectID)
			allowed = visible && authErr == nil
			projectReadable[assignment.ProjectID] = allowed
		}
		if !allowed {
			items[i].ProjectAssignment = nil
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "scope": scopeName(scope.Admin)})
}

type messageItem struct {
	FileFormats          []drive.FileFormat         `json:"file_formats,omitempty"`
	Resources            []messageResource          `json:"resources,omitempty"`
	ID                   string                     `json:"id"`
	ChatID               string                     `json:"chat_id"`
	ThreadID             string                     `json:"thread_id,omitempty"`
	ParentID             string                     `json:"parent_id,omitempty"`
	Type                 string                     `json:"type"`
	Body                 string                     `json:"body,omitempty"`
	DisplayText          string                     `json:"display_text,omitempty"`
	State                string                     `json:"state"`
	SentAt               time.Time                  `json:"sent_at"`
	Sender               directory.PersonProjection `json:"sender"`
	SenderKind           string                     `json:"sender_kind,omitempty"`
	BusinessReceipt      *ReceiptTarget             `json:"business_receipt,omitempty"`
	senderID, senderName string
}

const messageColumns = `message_id,chat_id,thread_id,parent_id,message_type,body,
	CASE WHEN message_type='post' OR (message_type='text' AND mention_names<>'{}'::jsonb)
		THEN search_text ELSE '' END AS display_text,
	sent_at,sender_id,sender_name,sender_kind,deleted`

func scanMessage(row pgx.CollectableRow) (messageItem, error) {
	var item messageItem
	var deleted bool
	err := row.Scan(&item.ID, &item.ChatID, &item.ThreadID, &item.ParentID, &item.Type, &item.Body, &item.DisplayText,
		&item.SentAt, &item.senderID, &item.senderName, &item.SenderKind, &deleted)
	item.SentAt = item.SentAt.UTC()
	item.State = "available"
	if deleted {
		item.State = "deleted"
	}
	return item, err
}

func (s *Service) projectMessages(ctx context.Context, p identity.Principal, scopeID string, scope authorizedScope, items []messageItem) error {
	refs := make([]directory.ResourcePersonRef, len(items))
	groups := make(map[string]bool, len(scope.Conversations))
	for _, c := range scope.Conversations {
		groups[c.ID] = c.Kind == orchestrator.ChatGroup
	}
	for i, item := range items {
		refs[i].OpenID = item.senderID
		refs[i].Name = item.senderName
		refs[i].GroupMember = groups[item.ChatID] && item.SenderKind == "user"
	}
	people, err := s.projectPeople(ctx, p, scopeID, refs)
	if err != nil {
		return err
	}
	for i, person := range people {
		items[i].Sender = person
	}
	type cloudIcon struct {
		message, format int
		token           string
	}
	var icons []cloudIcon
	var tokens []string
	documentItems := map[string]*messageItem{}
	for i := range items {
		item := &items[i]
		if item.State == "deleted" {
			continue
		}
		for _, ref := range drive.MessageReferences(item.Type, item.Body) {
			resource := messageResource{Kind: ref.Kind, Key: ref.Token, Name: ref.Name, URL: ref.URL}
			if ref.URL != "" {
				resource.Name = ""
				resource.State = "pending"
				documentItems[item.ID] = item
			}
			item.Resources = append(item.Resources, resource)
		}
		for _, ref := range drive.References(item.Type, item.Body) {
			if ref.Kind == "file" {
				icons = append(icons, cloudIcon{i, len(item.FileFormats), ref.Token})
				tokens = append(tokens, ref.Token)
			}
			item.FileFormats = append(item.FileFormats, drive.Format(ref.Kind, ref.Name))
		}
	}
	if len(documentItems) > 0 {
		if err := s.projectDocuments(ctx, documentItems); err != nil {
			return err
		}
	}
	if len(tokens) > 0 && s.files != nil {
		formats := s.files.CloudFormats(ctx, p, tokens)
		for _, icon := range icons {
			if format, ok := formats[icon.token]; ok {
				items[icon.message].FileFormats[icon.format] = format
			}
		}
	}
	s.receipts.project(ctx, p, items)
	return nil
}

func messageCursor(item messageItem) string {
	raw := strconv.FormatInt(item.SentAt.UnixMicro(), 10) + " " + item.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func parseCursor(encoded string) (*time.Time, string, bool) {
	if encoded == "" {
		return nil, "", true
	}
	if len(encoded) > 256 {
		return nil, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", false
	}
	stamp, id, ok := strings.Cut(string(raw), " ")
	micros, err := strconv.ParseInt(stamp, 10, 64)
	if !ok || err != nil || micros <= 0 || micros > 253402300799999999 || !validIdentifier(id) {
		return nil, "", false
	}
	at := time.UnixMicro(micros).UTC()
	return &at, id, true
}

func (s *Service) getMessageCount(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	s.writeMessageCount(w, r.Context(), p, `SELECT count(*) FROM feishu_chat_messages m WHERE `+
		visibleMessages+` AND NOT m.deleted`, s.tenant, s.app, scope.ids())
}

func (s *Service) getPersonalMessageCount(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	s.writeMessageCount(w, ctx, p, `SELECT count(*) FROM feishu_chat_messages m
		JOIN feishu_chat_conversations c USING (tenant_key,app_id,chat_id)
		WHERE `+visibleMessageRows+` AND NOT m.deleted
		AND NOT EXISTS (SELECT FROM feishu_chat_sync t
			WHERE (t.tenant_key,t.app_id,t.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
			AND t.thread_id='' AND t.source_blocked)
		AND ((c.kind='p2p' AND c.peer_open_id=$4) OR (c.kind='group' AND EXISTS (
			SELECT FROM feishu_drive_group_memberships gm
			WHERE (gm.tenant_key,gm.app_id,gm.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
			AND `+currentGroupMembership+`)))`,
		s.tenant, s.app, s.generation, p.OpenID)
}

func (s *Service) writeMessageCount(w http.ResponseWriter, ctx context.Context, p identity.Principal, query string, args ...any) {
	tx, err := s.readTx(ctx, p)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var total int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		sourceError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		sourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"total": total})
}

func (s *Service) getMessages(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	q, ok := searchQuery(r)
	search := strings.HasSuffix(r.URL.Path, "/search")
	limit, validLimit := requestedLimit(r, 50, 100)
	before, id, validCursor := parseCursor(r.URL.Query().Get("cursor"))
	if !ok || !validLimit || !validCursor || search && q == "" {
		writeError(w, 400, "invalid_chat_query")
		return
	}
	ctx := r.Context()
	tx, err := s.readTx(ctx, p)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	filter := visibleMessages + ` AND ($4='' OR (NOT deleted AND
		(strpos(lower(search_text),lower($4))>0 OR strpos(lower(sender_name),lower($4))>0)))`
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "group" && kind != "p2p" {
		writeError(w, 400, "invalid_chat_query")
		return
	}
	ids := make([]string, 0, len(scope.Conversations))
	for _, item := range scope.Conversations {
		if kind == "" || string(item.Kind) == kind {
			ids = append(ids, item.ID)
		}
	}
	args := []any{s.tenant, s.app, ids, q}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM feishu_chat_messages m WHERE `+filter, args...).Scan(&total); err != nil {
		sourceError(w, err)
		return
	}
	rows, err := tx.Query(ctx, `SELECT `+messageColumns+` FROM feishu_chat_messages m WHERE `+filter+
		` AND ($5::timestamptz IS NULL OR (sent_at,message_id)<($5,$6))
		ORDER BY sent_at DESC,message_id DESC LIMIT $7`, append(args, before, id, limit+1)...)
	if err != nil {
		sourceError(w, err)
		return
	}
	items, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		sourceError(w, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = messageCursor(items[limit-1])
	}
	if err := tx.Commit(ctx); err != nil {
		sourceError(w, err)
		return
	}
	if err := s.projectMessages(ctx, p, chatScopeID(r), scope, items); err != nil {
		sourceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "next_cursor": next})
}

// SearchProjectMessages returns only searchable snippets from a source-visible bound group.
func (s *Service) SearchProjectMessages(ctx context.Context, tx pgx.Tx, p identity.Principal, project, id, q string) (any, error) {
	if strings.TrimSpace(q) == "" || len([]rune(q)) > 200 {
		return nil, ErrAccessLost
	}
	if _, err := s.ProjectGroupForMutation(ctx, tx, p, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT m.message_id,m.sent_at,left(m.search_text,500) FROM feishu_chat_messages m
 JOIN project_chat_links l ON (l.tenant_key,l.app_id,l.chat_id)=(m.tenant_key,m.app_id,m.chat_id)
 WHERE `+visibleMessageRows+` AND m.chat_id=$3 AND l.project_id=$4 AND NOT m.deleted AND strpos(lower(m.search_text),lower($5))>0
 ORDER BY m.sent_at DESC,m.message_id LIMIT 25`, s.tenant, s.app, id, project, q)
	if err != nil {
		return nil, err
	}
	type snippet struct {
		ID   string    `json:"id"`
		Sent time.Time `json:"sent_at"`
		Text string    `json:"text"`
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[snippet])
	if err != nil {
		return nil, err
	}
	var bound bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_chat_links WHERE (tenant_key,app_id,chat_id,project_id)=($1,$2,$3,$4))", s.tenant, s.app, id, project).Scan(&bound); err != nil {
		return nil, err
	}
	if !bound {
		return nil, ErrAccessLost
	}
	return map[string]any{"items": items}, nil
}

func chatScopeID(r *http.Request) string {
	id := r.PathValue("chatID")
	if id == "" {
		return "chat-search"
	}
	return id
}

func (s *Service) getContext(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	messageID := r.PathValue("messageID")
	limit, ok := requestedLimit(r, 20, 50)
	if !ok || !validIdentifier(messageID) {
		writeError(w, 400, "invalid_chat_query")
		return
	}
	ctx := r.Context()
	tx, err := s.readTx(ctx, p)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	args := []any{s.tenant, s.app, scope.ids(), messageID}
	var deleted, threadHidden bool
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT sent_at,deleted, NOT visible_in_chat AND EXISTS (
		SELECT 1 FROM feishu_chat_sync t WHERE (t.tenant_key,t.app_id,t.chat_id,t.thread_id)=
			(m.tenant_key,m.app_id,m.chat_id,m.thread_id) AND t.thread_id<>'' AND t.source_blocked
	) FROM feishu_chat_messages m WHERE tenant_key=$1 AND app_id=$2 AND chat_id=ANY($3::text[]) AND message_id=$4`,
		args...).Scan(&at, &deleted, &threadHidden)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "message_not_found")
		return
	case err != nil:
		sourceError(w, err)
		return
	case threadHidden:
		sourceError(w, ErrAccessLost)
		return
	case deleted:
		writeError(w, 410, "message_deleted")
		return
	}
	rows, err := tx.Query(ctx, `SELECT * FROM (
		(SELECT `+messageColumns+` FROM feishu_chat_messages m WHERE `+visibleMessages+`
			AND (sent_at,message_id)<($5,$4) ORDER BY sent_at DESC,message_id DESC LIMIT $6)
		UNION ALL
		(SELECT `+messageColumns+` FROM feishu_chat_messages m WHERE `+visibleMessages+`
			AND (sent_at,message_id)>=($5,$4) ORDER BY sent_at,message_id LIMIT $6+1)
	) AS context_messages ORDER BY sent_at,message_id`, append(args, at, limit)...)
	if err != nil {
		sourceError(w, err)
		return
	}
	items, err := pgx.CollectRows(rows, scanMessage)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err == nil {
		err = s.projectMessages(ctx, p, chatScopeID(r), scope, items)
	}
	if err != nil {
		sourceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "anchor_id": messageID})
}

func scopeName(admin bool) string {
	if admin {
		return "all_authorized_bot_chats"
	}
	return "current_memberships_and_own_private_chats"
}

type syncDetail struct {
	ChatID             string     `json:"chat_id"`
	State              string     `json:"state"`
	Coverage           string     `json:"coverage"`
	AttemptedAt        *time.Time `json:"attempted_at"`
	CompletedAt        *time.Time `json:"completed_at"`
	CoveredUntil       *time.Time `json:"covered_until"`
	EarliestMessageAt  *time.Time `json:"earliest_message_at"`
	PendingThreads     int        `json:"pending_threads"`
	UnavailableThreads int        `json:"unavailable_threads"`
}

func (s *Service) getSync(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	ctx := r.Context()
	tx, err := s.readTx(ctx, p)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH earliest AS (
		SELECT chat_id,min(sent_at) AS at FROM feishu_chat_messages m WHERE `+visibleMessages+` AND NOT deleted GROUP BY chat_id
	) SELECT j.chat_id,j.state,j.attempted_at,j.completed_at,j.covered_until,e.at,
		j.full_at IS NOT NULL,
		count(t.thread_id) FILTER(WHERE t.state IN ('pending','syncing')),
		count(t.thread_id) FILTER(WHERE t.state IN ('partial','access_lost','failed'))
		FROM feishu_chat_sync j LEFT JOIN feishu_chat_sync t
			ON (t.tenant_key,t.app_id,t.chat_id)=(j.tenant_key,j.app_id,j.chat_id) AND t.thread_id<>''
		LEFT JOIN earliest e ON e.chat_id=j.chat_id
		WHERE j.tenant_key=$1 AND j.app_id=$2 AND j.chat_id=ANY($3::text[]) AND j.thread_id=''
		GROUP BY j.tenant_key,j.app_id,j.chat_id,j.thread_id,e.at ORDER BY j.chat_id`, s.tenant, s.app, scope.ids())
	if err != nil {
		sourceError(w, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (syncDetail, error) {
		var item syncDetail
		var full bool
		err := row.Scan(&item.ChatID, &item.State, &item.AttemptedAt, &item.CompletedAt, &item.CoveredUntil, &item.EarliestMessageAt,
			&full, &item.PendingThreads, &item.UnavailableThreads)
		item.Coverage = "partial"
		if full && item.PendingThreads == 0 && item.UnavailableThreads == 0 {
			item.Coverage = "all_source_visible_history"
		}
		if item.State == "complete" && item.Coverage == "partial" {
			item.State = "partial"
		}
		for _, t := range []*time.Time{item.AttemptedAt, item.CompletedAt, item.CoveredUntil, item.EarliestMessageAt} {
			if t != nil {
				*t = t.UTC()
			}
		}
		return item, err
	})
	if err != nil {
		sourceError(w, err)
		return
	}
	body := map[string]any{"items": items, "total": len(items), "scope": scopeName(scope.Admin)}
	if scope.Admin && r.PathValue("chatID") == "" && r.PathValue("projectID") == "" {
		var state string
		err := tx.QueryRow(ctx, `SELECT state FROM feishu_chat_discovery WHERE tenant_key=$1 AND app_id=$2`,
			s.tenant, s.app).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			state = "pending"
		} else if err != nil {
			sourceError(w, err)
			return
		}
		body["discovery_state"] = state
	}
	writeJSON(w, 200, body)
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request, _ identity.Principal, scope authorizedScope) {
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.assertEnabled(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE feishu_chat_sync SET next_at=LEAST(next_at,clock_timestamp())
			WHERE tenant_key=$1 AND app_id=$2 AND chat_id=ANY($3::text[])`, s.tenant, s.app, scope.ids())
		if err != nil {
			return err
		}
		if scope.Admin && r.PathValue("chatID") == "" && r.PathValue("projectID") == "" {
			_, err = tx.Exec(ctx, `INSERT INTO feishu_chat_discovery (tenant_key,app_id) VALUES ($1,$2)
				ON CONFLICT (tenant_key,app_id) DO UPDATE
				SET next_at=LEAST(feishu_chat_discovery.next_at,clock_timestamp())`, s.tenant, s.app)
		}
		return err
	})
	if err != nil {
		sourceError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"state": "queued", "total": len(scope.Conversations)})
}
