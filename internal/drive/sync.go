package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type GroupAccess func(context.Context, identity.Principal, string, string) (bool, error)

type Service struct {
	canReadGroup GroupAccess
	pool         *pgxpool.Pool
	tenant, app  string
	generation   int64
	roles        *access.Service
	source       Source
	objects      ObjectStore
}

func New(pool *pgxpool.Pool, tenant, app string, generation int64, source Source, roles *access.Service, objects ObjectStore, canReadGroup GroupAccess) *Service {
	return &Service{pool: pool, tenant: tenant, app: app, generation: generation, roles: roles, source: source, objects: objects, canReadGroup: canReadGroup}
}

func (s *Service) index(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT message_id,message_type,body,chat_id FROM feishu_chat_messages m
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND NOT deleted AND NOT files_indexed
 AND NOT EXISTS(SELECT FROM feishu_candidate_event_owners o
    WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL)
 AND EXISTS(SELECT FROM project_chat_links l JOIN feishu_chat_conversations c USING(tenant_key,app_id,chat_id)
 WHERE l.tenant_key=$1 AND l.app_id=$2 AND l.chat_id=m.chat_id AND c.kind='group')
 AND EXISTS(SELECT FROM feishu_bot_configs WHERE tenant_key=$1 AND app_id=$2 AND enabled AND credentials_generation=$3)
 ORDER BY sent_at LIMIT 64 FOR UPDATE SKIP LOCKED`, s.tenant, s.app, s.generation)
	if err != nil {
		return false, err
	}
	msgs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Kind, Body, Chat string }])
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		ids := []int64{}
		refs := References(m.Kind, m.Body)
		if _, err = tx.Exec(ctx, `DELETE FROM feishu_message_wiki_links WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3`, s.tenant, s.app, m.ID); err != nil {
			return false, err
		}
		for _, ref := range refs {
			if ref.Kind == "wiki" {
				ref, err = s.indexWiki(ctx, tx, m.ID, ref)
				if err != nil {
					return false, err
				}
				if ref.Kind == "" {
					continue
				}
			}
			scope := ""
			if messageResource(ref.Kind) {
				scope = s.app
			}
			var id int64
			err = tx.QueryRow(ctx, `WITH legacy AS (
                SELECT f.id FROM feishu_files f WHERE $2<>'' AND f.tenant_key=$1 AND f.app_scope='' AND f.kind=$3 AND f.token=$4
                AND EXISTS(SELECT FROM feishu_file_shares s WHERE s.file_id=f.id AND s.tenant_key=$1 AND s.app_id=$2)
            ), created AS (
                INSERT INTO feishu_files(tenant_key,app_scope,kind,token) SELECT $1,$2,$3,$4 WHERE NOT EXISTS(SELECT FROM legacy)
                ON CONFLICT(tenant_key,app_scope,kind,token) DO UPDATE SET token=EXCLUDED.token RETURNING id
            ) SELECT id FROM legacy UNION ALL SELECT id FROM created`, s.tenant, scope, ref.Kind, ref.Token).Scan(&id)
			if err != nil {
				return false, err
			}
			ids = append(ids, id)
			tag, err := tx.Exec(ctx, `INSERT INTO feishu_file_shares(tenant_key,app_id,message_id,file_id,name,declared_size)
   VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_key,app_id,message_id,file_id) DO UPDATE SET present=true,state='pending',version_id=NULL,attempted_at=NULL,next_at=now(),name=EXCLUDED.name,declared_size=EXCLUDED.declared_size
 WHERE NOT feishu_file_shares.present`, s.tenant, s.app, m.ID, id, ref.Name, ref.Size)
			if err != nil {
				return false, err
			}
			if tag.RowsAffected() == 0 && ref.Kind == "attachment" {
				_, err = tx.Exec(ctx, `UPDATE feishu_file_shares SET name=$5,declared_size=$6
     WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3 AND file_id=$4`, s.tenant, s.app, m.ID, id, ref.Name, ref.Size)
				if err != nil {
					return false, err
				}
			}
			if _, err = tx.Exec(ctx, `INSERT INTO feishu_file_project_revisions(tenant_key,app_id,file_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, s.tenant, s.app, id); err != nil {
				return false, err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE feishu_file_shares SET present=false,state='revoked' WHERE present AND tenant_key=$1 AND app_id=$2 AND message_id=$3 AND NOT(file_id=ANY($4))`, s.tenant, s.app, m.ID, ids)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE feishu_chat_messages SET files_indexed=true WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3`, s.tenant, s.app, m.ID)
		if err != nil {
			return false, err
		}
	}
	return len(msgs) > 0, tx.Commit(ctx)
}

type job struct {
	Ref
	id      int64
	chat    string
	attempt time.Time
}

func (s *Service) refresh(ctx context.Context) (bool, error) {
	var j job
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT x.* FROM feishu_file_sources x
 JOIN feishu_file_shares s USING(tenant_key,app_id,message_id,file_id)
 JOIN project_chat_links l USING(tenant_key,app_id,chat_id)
 WHERE x.chat_kind='group' AND x.tenant_key=$1 AND x.app_id=$2 AND x.credentials_generation=$3 AND x.next_at<=now()
 ORDER BY x.next_at,x.file_id LIMIT 1 FOR UPDATE OF s SKIP LOCKED)
 UPDATE feishu_file_shares s SET attempted_at=clock_timestamp(),next_at=now()+interval '2 minutes'
 FROM candidate c WHERE (s.tenant_key,s.app_id,s.message_id,s.file_id)=(c.tenant_key,c.app_id,c.message_id,c.file_id)
 RETURNING c.file_id,c.kind,c.token,c.name,c.chat_id,c.chat_kind,c.message_id,c.declared_size,s.attempted_at`, s.tenant, s.app, s.generation).
		Scan(&j.id, &j.Kind, &j.Token, &j.Name, &j.chat, &j.ChatKind, &j.MessageID, &j.Size, &j.attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fetch, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	var snap Snapshot
	failure := s.source.CheckShare(fetch, j.Ref, j.chat)
	if failure == nil {
		snap, failure = s.source.ReadFile(fetch, j.Ref, false)
	}
	if snap.Stream != nil {
		defer snap.Stream.Close()
	}
	var obj storedObject
	if failure == nil {
		var body io.Reader = bytes.NewReader(snap.Content)
		if snap.Stream != nil {
			body = snap.Stream
		}
		obj, failure = s.storeObject(fetch, body, j.Size)
	}
	if failure == nil {
		failure = s.source.CheckShare(fetch, j.Ref, j.chat)
	}
	if failure == nil {
		_, failure = s.source.ReadFile(fetch, j.Ref, true)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var previous *int64
	var failures int
	err = tx.QueryRow(ctx, `SELECT s.version_id,s.failures FROM feishu_file_shares s JOIN feishu_file_sources x
 USING(tenant_key,app_id,message_id,file_id) WHERE x.tenant_key=$1 AND x.app_id=$2 AND x.message_id=$3 AND x.file_id=$4
 AND x.attempted_at=$5 AND x.credentials_generation=$6
 AND EXISTS(SELECT FROM project_chat_links l WHERE (l.tenant_key,l.app_id,l.chat_id)=(x.tenant_key,x.app_id,x.chat_id)) FOR UPDATE OF s`, s.tenant, s.app, j.MessageID, j.id, j.attempt, s.generation).Scan(&previous, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	version := previous
	state := "complete"
	delay := 5 * time.Minute
	failures = min(failures+1, 10)
	switch {
	case failure == nil:
		if err := fetch.Err(); err != nil {
			return false, err
		}
		tag, err := tx.Exec(ctx, `UPDATE feishu_sync_objects SET published=true WHERE tenant_key=$1 AND object_key=$2 AND (published OR reserved_until>now())`, s.tenant, obj.key)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, ErrUnavailable
		}
		var stored int64
		err = tx.QueryRow(ctx, `INSERT INTO feishu_file_versions(file_id,digest,chunks,byte_size,object_key,content_type)
        VALUES($1,$2,ARRAY[]::bytea[],$3,$4,$5) ON CONFLICT(file_id,digest) DO UPDATE
        SET object_key=EXCLUDED.object_key,content_type=EXCLUDED.content_type RETURNING id`, j.id, obj.digest, obj.size, obj.key, obj.mime).Scan(&stored)
		if err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO feishu_file_share_versions VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, s.tenant, s.app, j.MessageID, j.id, stored); err != nil {
			return false, err
		}
		version = &stored
		failures = 0
	case errors.Is(failure, errOverLimit):
		state = "over_limit"
		delay = 24 * time.Hour
	case errors.Is(failure, ErrRevoked):
		state = "revoked"
	case errors.Is(failure, ErrPartial):
		state = "partial"
		delay = min(time.Minute<<(failures-1), 30*time.Minute)
	default:
		state = "failed"
		if previous != nil {
			state = "expired"
		}
		delay = min(30*time.Second<<(failures-1), 30*time.Minute)
		var throttle *RetryAfterError
		if errors.As(failure, &throttle) {
			delay = max(delay, throttle.Delay)
		}
	}
	_, err = tx.Exec(ctx, `UPDATE feishu_file_shares SET state=$5,version_id=$6,name=coalesce(nullif($7,''),name),
 synced_at=CASE WHEN $5='complete' THEN attempted_at ELSE synced_at END,
 next_at=now()+make_interval(secs=>$8),failures=$9 WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3 AND file_id=$4`,
		s.tenant, s.app, j.MessageID, j.id, state, version, strings.ReplaceAll(snap.Name, "\x00", ""), delay.Seconds(), failures)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *Service) Run(ctx context.Context) {
	var nextCleanup time.Time
	for ctx.Err() == nil {
		if time.Now().After(nextCleanup) {
			if err := s.discardAbandoned(ctx); err != nil {
				slog.WarnContext(ctx, "待发布文件对象回收失败，将自动重试")
			}
			nextCleanup = time.Now().Add(time.Minute)
		}
		indexed, e := s.index(ctx)
		wiki, wikiErr := s.resolveWiki(ctx)
		worked, err := s.refresh(ctx)
		if e != nil || err != nil || wikiErr != nil {
			slog.WarnContext(ctx, "飞书文件同步暂未完成，将自动重试")
		}
		delay := 2 * time.Second
		if (worked || indexed || wiki) && err == nil && e == nil {
			delay = 50 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
