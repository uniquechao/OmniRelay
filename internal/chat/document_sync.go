package chat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/drive"
)

const documentBotEnabled = `EXISTS (SELECT FROM feishu_bot_configs
	WHERE tenant_key=$1 AND app_id=$2 AND credentials_generation=$3 AND enabled)`

// 新旧消息共用索引，先提交来源关系，再由工作进程领取单个文档。
func (s *Service) indexDocuments(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT message_id,message_type,body FROM feishu_chat_messages m
			WHERE tenant_key=$1 AND app_id=$2 AND NOT deleted AND NOT documents_indexed AND body LIKE '%https:%'
			AND `+documentBotEnabled+` AND NOT EXISTS (SELECT FROM feishu_candidate_event_owners o WHERE
			(o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL)
			ORDER BY sent_at DESC LIMIT 64 FOR UPDATE SKIP LOCKED`, s.tenant, s.app, s.generation)
		if err != nil {
			return err
		}
		messages, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Kind, Body string }])
		if err != nil {
			return err
		}
		for _, m := range messages {
			for _, ref := range drive.MessageReferences(m.Kind, m.Body) {
				if ref.URL == "" {
					continue
				}
				_, err = tx.Exec(ctx, `INSERT INTO feishu_chat_documents (tenant_key,app_id,message_id,kind,token)
					VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, s.tenant, s.app, m.ID, ref.Kind, ref.Token)
				if err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE feishu_chat_messages SET documents_indexed=true
				WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3`, s.tenant, s.app, m.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) syncDocument(ctx context.Context) (bool, error) {
	var ref drive.Ref
	var chat string
	var lease time.Time
	err := s.pool.QueryRow(ctx, `WITH job AS (
		SELECT d.message_id,d.kind,d.token,m.chat_id,c.kind AS chat_kind FROM feishu_chat_documents d
		JOIN feishu_chat_messages m USING (tenant_key,app_id,message_id)
		JOIN feishu_chat_conversations c USING (tenant_key,app_id,chat_id)
		WHERE d.tenant_key=$1 AND d.app_id=$2 AND NOT m.deleted AND `+documentBotEnabled+`
		AND (d.next_at<=now() OR d.generation<>$3)
		ORDER BY d.next_at LIMIT 1 FOR UPDATE OF d SKIP LOCKED)
		UPDATE feishu_chat_documents d SET next_at=clock_timestamp()+interval '2 minutes',generation=$3,
		state=CASE WHEN generation=$3 THEN state ELSE 'pending' END,
		content=CASE WHEN generation=$3 THEN content END,title=CASE WHEN generation=$3 THEN title ELSE '' END,
		object_kind=CASE WHEN generation=$3 THEN object_kind ELSE '' END
		FROM job j WHERE (d.tenant_key,d.app_id,d.message_id,d.kind,d.token)=($1,$2,j.message_id,j.kind,j.token)
		RETURNING j.message_id,j.kind,j.token,j.chat_id,j.chat_kind,d.next_at`, s.tenant, s.app, s.generation).
		Scan(&ref.MessageID, &ref.Kind, &ref.Token, &chat, &ref.ChatKind, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fetch, cancel := context.WithTimeout(ctx, 85*time.Second)
	defer cancel()
	object := ref
	var snap drive.Snapshot
	var content []byte
	failure := s.preview.CheckPreviewShare(fetch, ref, chat)
	if failure == nil && ref.Kind == "wiki" {
		object, failure = s.preview.ResolveDocumentWiki(fetch, ref)
	}
	if failure == nil {
		snap, failure = s.preview.ReadDocument(fetch, object)
		if failure == nil {
			content, failure = drive.ReadPreviewBytes(snap)
		}
	}
	state, delay := "ready", time.Hour
	if failure != nil {
		state, delay = "source_unavailable", time.Minute
		switch {
		case errors.Is(failure, drive.ErrAppScopeMissing):
			state, delay = "application_scope_missing", 5*time.Minute
		case errors.Is(failure, drive.ErrRevoked), errors.Is(failure, drive.ErrPreviewMessageDeleted):
			state, delay = "resource_forbidden", 5*time.Minute
		case errors.Is(failure, drive.ErrPartial):
			state, delay = "over_limit", time.Hour
		case errors.Is(failure, drive.ErrPreviewUnsupported):
			state, delay = "unsupported", time.Hour
		}
		var limited *drive.RetryAfterError
		if errors.As(failure, &limited) {
			delay = max(delay, limited.Delay)
		}
	}
	title := snap.Name
	if title == "" {
		title = object.Name
	}
	// 撤回、编辑、凭据轮换或租约替换后，旧读取结果不能重新发布。
	_, err = s.pool.Exec(ctx, `UPDATE feishu_chat_documents SET object_kind=$7,title=$8,state=$9,content=$10,
		next_at=now()+$11*interval '1 second' WHERE tenant_key=$1 AND app_id=$2 AND generation=$3
		AND message_id=$4 AND kind=$5 AND token=$6 AND next_at=$12 AND `+documentBotEnabled,
		s.tenant, s.app, s.generation, ref.MessageID, ref.Kind, ref.Token, object.Kind, cleanText(title), state, content, int64(delay.Seconds()), lease)
	return true, err
}

func (s *Service) RunDocuments(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := s.indexDocuments(ctx)
		var worked bool
		if err == nil {
			worked, err = s.syncDocument(ctx)
		}
		delay := 2 * time.Second
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "聊天文档同步暂时失败")
		}
		if worked && err == nil {
			delay = 200 * time.Millisecond
		}
		timer.Reset(delay)
	}
}

// 只投影当前消息页中的标题和状态，正文经原消息预览接口单独读取。
func (s *Service) projectDocuments(ctx context.Context, items map[string]*messageItem) error {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	rows, err := s.pool.Query(ctx, `SELECT message_id,kind,token,title,object_kind,state FROM feishu_chat_documents
		WHERE tenant_key=$1 AND app_id=$2 AND generation=$3 AND message_id=ANY($4)`, s.tenant, s.app, s.generation, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, key, title, object, state string
		if err := rows.Scan(&id, &kind, &key, &title, &object, &state); err != nil {
			return err
		}
		for i := range items[id].Resources {
			r := &items[id].Resources[i]
			if r.Kind == kind && r.Key == key {
				r.Name, r.ObjectKind, r.State = title, object, state
				break
			}
		}
	}
	return rows.Err()
}
