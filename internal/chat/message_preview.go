package chat

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type MessagePreviewSource interface {
	CheckPreviewShare(context.Context, drive.Ref, string) error
	ResolveDocumentWiki(context.Context, drive.Ref) (drive.Ref, error)
	ReadDocument(context.Context, drive.Ref) (drive.Snapshot, error)
	ReadFile(context.Context, drive.Ref, bool) (drive.Snapshot, error)
}

func (s *Service) ConfigurePreview(source MessagePreviewSource) {
	s.preview = source
}

type messageResource struct {
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	ObjectKind string `json:"object_kind,omitempty"`
	State      string `json:"state,omitempty"`
}

var errMessageResourceMissing = errors.New("message resource not found")

func (s *Service) archivedPreviewResource(ctx context.Context, p identity.Principal, c conversation, id, kind, key string) (drive.Ref, error) {
	tx, err := s.readTx(ctx, p)
	if err != nil {
		return drive.Ref{}, err
	}
	defer tx.Rollback(ctx)
	var typ, body string
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT message_type,body,deleted FROM feishu_chat_messages m WHERE `+visibleMessages+
		` AND message_id=$4`, s.tenant, s.app, []string{c.ID}, id).Scan(&typ, &body, &deleted)
	if err != nil {
		return drive.Ref{}, err
	}
	if deleted {
		return drive.Ref{}, drive.ErrPreviewMessageDeleted
	}
	for _, ref := range drive.MessageReferences(typ, body) {
		if ref.Kind == kind && ref.Token == key {
			ref.MessageID, ref.ChatKind = id, string(c.Kind)
			return ref, nil
		}
	}
	return drive.Ref{}, errMessageResourceMissing
}

func messagePreviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, drive.ErrPreviewMessageDeleted):
		writeError(w, 410, "message_deleted")
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, errMessageResourceMissing):
		writeError(w, 404, "resource_not_found")
	case errors.Is(err, drive.ErrRevoked):
		writeError(w, 403, "resource_forbidden")
	case errors.Is(err, drive.ErrPreviewUnsupported):
		writeError(w, 415, "unsupported")
	case errors.Is(err, drive.ErrPartial):
		writeError(w, 413, "over_limit")
	case errors.Is(err, drive.ErrUnavailable):
		writeError(w, 503, "source_unavailable")
	default:
		sourceError(w, err)
	}
}

func (s *Service) getMessagePreview(w http.ResponseWriter, r *http.Request, p identity.Principal, scope authorizedScope) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, kind, key := r.PathValue("messageID"), r.PathValue("kind"), r.PathValue("resource")
	options, ok := drive.MessagePreviewParams(r)
	if !ok || !validIdentifier(id) || len(scope.Conversations) != 1 || len(key) > 256 {
		writeError(w, 400, "invalid_preview")
		return
	}
	if s.preview == nil {
		writeError(w, 503, "source_unavailable")
		return
	}
	ctx := r.Context()
	chat := scope.Conversations[0]
	ref, err := s.archivedPreviewResource(ctx, p, chat, id, kind, key)
	if err == nil {
		err = s.preview.CheckPreviewShare(ctx, ref, chat.ID)
	}
	if err != nil {
		messagePreviewError(w, err)
		return
	}
	if kind == "image" {
		s.writeMessageImage(w, r, p, chat, ref)
		return
	}
	var result drive.MessagePreviewResult
	if ref.URL != "" {
		result, err = s.readDocumentSnapshot(ctx, ref, options)
	} else {
		result, err = drive.RenderMessagePreview(ctx, options, ref, func() (drive.Snapshot, error) {
			return s.preview.ReadFile(ctx, ref, false)
		})
	}
	if err == nil {
		_, err = s.archivedPreviewResource(ctx, p, chat, id, kind, key)
	}
	if err == nil && ref.URL != "" && r.PathValue("projectID") == "" {
		// 项目入口由响应包装器复核；机器人记录也在返回正文前复核当前成员资格。
		_, err = s.authorize(ctx, p, chat.ID)
	}
	if err != nil {
		messagePreviewError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Service) readDocumentSnapshot(ctx context.Context, ref drive.Ref, options drive.MessagePreviewOptions) (drive.MessagePreviewResult, error) {
	var state, kind string
	var snap drive.Snapshot
	err := s.pool.QueryRow(ctx, `SELECT state,object_kind,title,content FROM feishu_chat_documents
		WHERE tenant_key=$1 AND app_id=$2 AND generation=$3 AND message_id=$4 AND kind=$5 AND token=$6`,
		s.tenant, s.app, s.generation, ref.MessageID, ref.Kind, ref.Token).Scan(&state, &kind, &snap.Name, &snap.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return drive.MessagePreviewResult{Stage: "pending"}, nil
	}
	if err != nil {
		return drive.MessagePreviewResult{}, err
	}
	if state != "ready" {
		return drive.MessagePreviewResult{Stage: state}, nil
	}
	ref.Kind, ref.Name = kind, snap.Name
	return drive.RenderMessagePreview(ctx, options, ref, func() (drive.Snapshot, error) { return snap, nil })
}

func (s *Service) writeMessageImage(w http.ResponseWriter, r *http.Request, p identity.Principal, chat conversation, ref drive.Ref) {
	ctx := r.Context()
	snapshot, err := s.preview.ReadFile(ctx, ref, false)
	var data []byte
	if err == nil {
		data, err = drive.ReadPreviewBytes(snapshot)
	}
	if err == nil {
		_, err = s.archivedPreviewResource(ctx, p, chat, ref.MessageID, ref.Kind, ref.Token)
	}
	if err != nil {
		messagePreviewError(w, err)
		return
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/x-icon":
	default:
		messagePreviewError(w, drive.ErrPreviewUnsupported)
		return
	}
	if !validateProjectRead(w) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	w.Header().Set("Content-Type", mime)
	_, _ = w.Write(data)
}
