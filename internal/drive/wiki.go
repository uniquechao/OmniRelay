package drive

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type wikiResolver interface {
	ResolveWiki(context.Context, string) (Ref, error)
}

func (s *Service) indexWiki(ctx context.Context, tx pgx.Tx, message string, ref Ref) (Ref, error) {
	var target Ref
	err := tx.QueryRow(ctx, `INSERT INTO feishu_wiki_targets(tenant_key,app_id,token) VALUES($1,$2,$3)
    ON CONFLICT(tenant_key,app_id,token) DO UPDATE SET token=EXCLUDED.token
    RETURNING coalesce(kind,''),coalesce(object_token,'')`, s.tenant, s.app, ref.Token).Scan(&target.Kind, &target.Token)
	if err != nil {
		return target, err
	}
	target.Name = target.Token
	_, err = tx.Exec(ctx, `INSERT INTO feishu_message_wiki_links VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.tenant, s.app, message, ref.Token)
	return target, err
}

// 节点映射可变化；领取和发布分别提交，平台请求不持有消息行锁。
func (s *Service) resolveWiki(ctx context.Context) (bool, error) {
	resolver, ok := s.source.(wikiResolver)
	if !ok {
		return false, nil
	}
	var token, kind, object string
	var lease time.Time
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
        SELECT w.token FROM feishu_wiki_targets w WHERE tenant_key=$1 AND app_id=$2 AND next_at<=now()
        AND EXISTS(SELECT FROM feishu_message_wiki_links n JOIN feishu_chat_messages m USING(tenant_key,app_id,message_id)
        JOIN feishu_chat_conversations g USING(tenant_key,app_id,chat_id)
        JOIN feishu_chat_sync root USING(tenant_key,app_id,chat_id)
        WHERE (n.tenant_key,n.app_id,n.token)=(w.tenant_key,w.app_id,w.token) AND g.kind='group'
        AND root.thread_id='' AND NOT root.source_blocked AND NOT m.deleted
        AND NOT EXISTS(SELECT FROM feishu_candidate_event_owners o
            WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL))
        AND EXISTS(SELECT FROM feishu_bot_configs WHERE tenant_key=$1 AND app_id=$2 AND enabled AND credentials_generation=$3)
        ORDER BY next_at LIMIT 1 FOR UPDATE SKIP LOCKED)
        UPDATE feishu_wiki_targets w SET next_at=clock_timestamp()+interval '2 minutes' FROM candidate c
        WHERE tenant_key=$1 AND app_id=$2 AND w.token=c.token RETURNING w.token,coalesce(kind,''),coalesce(object_token,''),next_at`, s.tenant, s.app, s.generation).Scan(&token, &kind, &object, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fetch, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target, failure := resolver.ResolveWiki(fetch, token)
	if failure != nil && !errors.Is(failure, ErrRevoked) {
		return true, failure
	}
	return true, pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE feishu_wiki_targets SET kind=$4,object_token=$5,next_at=now()+interval '5 minutes'
        WHERE tenant_key=$1 AND app_id=$2 AND token=$3 AND next_at=$6
        AND EXISTS(SELECT FROM feishu_bot_configs WHERE tenant_key=$1 AND app_id=$2 AND enabled AND credentials_generation=$7)`, s.tenant, s.app, token, target.Kind, target.Token, lease, s.generation)
		if err != nil || tag.RowsAffected() == 0 || (kind == target.Kind && object == target.Token) {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE feishu_chat_messages m SET files_indexed=false FROM feishu_message_wiki_links n
        WHERE (m.tenant_key,m.app_id,m.message_id)=(n.tenant_key,n.app_id,n.message_id)
        AND (n.tenant_key,n.app_id,n.token)=($1,$2,$3) AND NOT m.deleted`, s.tenant, s.app, token)
		return err
	})
}
