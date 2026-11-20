package chat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type Service struct {
	files           *drive.Service
	pool            *pgxpool.Pool
	tenant, app     string
	generation      int64
	source          Source
	people          *directory.Service
	receipts        ReceiptSources
	delivery        DeliverySource
	deliveryEnabled bool
	check           func(context.Context, identity.Principal, string, string) (bool, error)
}

func New(pool *pgxpool.Pool, tenant, app string, generation int64, source Source,
	people *directory.Service, check func(context.Context, identity.Principal, string, string) (bool, error), receipts ReceiptSources,
) *Service {
	return &Service{pool: pool, tenant: tenant, app: app, generation: generation, source: source, people: people, check: check, receipts: receipts}
}

func (s *Service) ConfigureFiles(files *drive.Service) { s.files = files }

type conversation struct {
	assignmentVersion int64
	ID                string                `json:"id"`
	Kind              orchestrator.ChatKind `json:"kind"`
	Name              string                `json:"name"`
	Peer              string                `json:"-"`
}

func (s *Service) upsertConversation(ctx context.Context, tx pgx.Tx, id string, kind orchestrator.ChatKind, peer, name string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO feishu_chat_conversations AS c
		(tenant_key, app_id, chat_id, kind, peer_open_id, name) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_key, app_id, chat_id) DO UPDATE SET name=COALESCE(NULLIF(EXCLUDED.name,''),c.name)
		WHERE (c.kind,c.peer_open_id)=(EXCLUDED.kind,EXCLUDED.peer_open_id)`, s.tenant, s.app, id, kind, peer, cleanText(name))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAccessLost
	}
	return s.ensureCursor(ctx, tx, id, "")
}

func (s *Service) ensureCursor(ctx context.Context, tx pgx.Tx, chatID, threadID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO feishu_chat_sync (tenant_key,app_id,chat_id,thread_id)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.tenant, s.app, chatID, threadID)
	return err
}

// AcceptEvent runs in the SDK connection's fenced archive transaction.
func (s *Service) AcceptEvent(ctx context.Context, tx pgx.Tx, event orchestrator.ArchiveEvent) error {
	if event.Kind != "" {
		if err := s.upsertConversation(ctx, tx, event.ChatID, event.Kind, event.PeerOpenID, ""); err != nil {
			return err
		}
	}
	if event.Message == nil {
		return nil
	}
	if event.Message.ChatID != event.ChatID {
		return ErrUnavailable
	}
	if err := s.saveMessage(ctx, tx, *event.Message); err != nil {
		return err
	}
	if event.Kind == orchestrator.ChatGroup && event.Message.ThreadID != "" {
		return s.ensureCursor(ctx, tx, event.ChatID, event.Message.ThreadID)
	}
	return nil
}

func (s *Service) saveMessage(ctx context.Context, tx pgx.Tx, msg orchestrator.ChatMessage) error {
	if msg.ID == "" || msg.ChatID == "" || msg.SentAt.IsZero() {
		return ErrUnavailable
	}
	var search string
	if msg.Deleted {
		msg.Body = ""
	} else {
		search = searchableText(msg.Type, msg.Body)
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.SentAt
	}
	// A tombstone wins even if the recall arrived before the original message.
	// Deleted history pages and live callbacks share this conflict key.
	_, err := tx.Exec(ctx, `INSERT INTO feishu_chat_messages AS m
		(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,sender_id,sender_kind,sender_name,
		message_type,thread_id,parent_id,body,search_text,deleted,visible_in_chat)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (tenant_key,app_id,message_id) DO UPDATE SET
			sent_at=CASE WHEN EXCLUDED.deleted AND EXCLUDED.message_type='' THEN m.sent_at ELSE EXCLUDED.sent_at END,
			updated_at=GREATEST(m.updated_at,EXCLUDED.updated_at),
			sender_id=COALESCE(NULLIF(EXCLUDED.sender_id,''),m.sender_id),
			sender_kind=COALESCE(NULLIF(EXCLUDED.sender_kind,''),m.sender_kind),
			sender_name=COALESCE(NULLIF(EXCLUDED.sender_name,''),m.sender_name),
			message_type=COALESCE(NULLIF(EXCLUDED.message_type,''),m.message_type),
			thread_id=COALESCE(NULLIF(EXCLUDED.thread_id,''),m.thread_id),
			parent_id=COALESCE(NULLIF(EXCLUDED.parent_id,''),m.parent_id),
			body=CASE WHEN m.deleted OR EXCLUDED.deleted THEN '' ELSE EXCLUDED.body END,
			search_text=CASE WHEN m.deleted OR EXCLUDED.deleted THEN '' ELSE EXCLUDED.search_text END,
			deleted=m.deleted OR EXCLUDED.deleted,
			visible_in_chat=m.visible_in_chat OR EXCLUDED.visible_in_chat
		WHERE m.chat_id=EXCLUDED.chat_id AND (
			(m.deleted AND m.message_type='' AND EXCLUDED.message_type<>'')
			OR (NOT m.deleted AND (EXCLUDED.deleted OR EXCLUDED.updated_at >= m.updated_at))
		)`,
		s.tenant, s.app, msg.ID, msg.ChatID, msg.SentAt, msg.UpdatedAt, msg.SenderID, msg.SenderKind, cleanText(msg.SenderName),
		msg.Type, msg.ThreadID, msg.ParentID, cleanText(msg.Body), cleanText(search), msg.Deleted, msg.VisibleInChat)
	return err
}

func cleanText(text string) string { return strings.ReplaceAll(text, "\x00", "") }

func searchableText(kind, body string) string {
	var content any
	if json.Unmarshal([]byte(body), &content) != nil {
		if kind == "text" {
			return body
		}
		return ""
	}
	var parts []string
	var collect func(any)
	collect = func(node any) {
		switch node := node.(type) {
		case []any:
			for _, child := range node {
				collect(child)
			}
		case map[string]any:
			for _, key := range []string{"text", "title", "content", "file_name", "name", "elements", "header"} {
				collect(node[key])
			}
			// Rich text can wrap its elements in a language key.
			for _, key := range []string{"zh_cn", "en_us", "ja_jp"} {
				collect(node[key])
			}
		case string:
			parts = append(parts, node)
		}
	}
	collect(content)
	return strings.Join(parts, " ")
}

func (s *Service) listConversations(ctx context.Context, id, project string) ([]conversation, error) {
	rows, err := s.pool.Query(ctx, `SELECT chat_id,kind,name,peer_open_id,project_assignment_version
		FROM feishu_chat_conversations c JOIN feishu_bot_configs b USING (tenant_key,app_id)
		WHERE c.tenant_key=$1 AND c.app_id=$2 AND b.enabled AND b.credentials_generation=$3
			AND ($4='' OR chat_id=$4) AND ($5='' OR EXISTS (
                SELECT 1 FROM project_chat_assignments a WHERE (a.tenant_key,a.app_id,a.chat_id,a.project_id)=(c.tenant_key,c.app_id,c.chat_id,$5)))
            ORDER BY chat_id`, s.tenant, s.app, s.generation, id, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversation, error) {
		var item conversation
		err := row.Scan(&item.ID, &item.Kind, &item.Name, &item.Peer, &item.assignmentVersion)
		return item, err
	})
}

type syncCursor struct {
	ChatID, ThreadID, Token string
	Kind                    orchestrator.ChatKind
	Start, End              *time.Time
}
