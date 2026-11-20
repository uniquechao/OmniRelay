package drive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
)

type Service struct {
	pool        *pgxpool.Pool
	tenant, app string
	generation  int64
	roles       *access.Service
	source      Source
}

func New(pool *pgxpool.Pool, tenant, app string, generation int64, source Source, roles *access.Service) *Service {
	return &Service{pool, tenant, app, generation, roles, source}
}

func (s *Service) index(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT message_id,message_type,body,chat_id FROM feishu_chat_messages
 WHERE tenant_key=$1 AND app_id=$2 AND NOT deleted AND NOT files_indexed
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
		if len(refs) > 0 {
			if _, err = tx.Exec(ctx, `SELECT FROM feishu_chat_conversations WHERE tenant_key=$1 AND app_id=$2 AND chat_id=$3 FOR UPDATE`, s.tenant, s.app, m.Chat); err != nil {
				return false, err
			}
		}
		for _, ref := range refs {
			var id int64
			err = tx.QueryRow(ctx, `INSERT INTO feishu_files(tenant_key,kind,token) VALUES($1,$2,$3)
   ON CONFLICT(tenant_key,kind,token) DO UPDATE SET token=EXCLUDED.token RETURNING id`, s.tenant, ref.Kind, ref.Token).Scan(&id)
			if err != nil {
				return false, err
			}
			ids = append(ids, id)
			tag, err := tx.Exec(ctx, `INSERT INTO feishu_file_shares(tenant_key,app_id,message_id,file_id,name,declared_size)
   VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_key,app_id,message_id,file_id) DO UPDATE SET present=true,next_at=now(),name=EXCLUDED.name,declared_size=EXCLUDED.declared_size
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
			if tag.RowsAffected() > 0 {
				_, err = tx.Exec(ctx, `INSERT INTO feishu_file_projects SELECT tenant_key,app_id,$4,project_id FROM project_chat_links
    WHERE tenant_key=$1 AND app_id=$2 AND chat_id=$3 ON CONFLICT DO NOTHING`, s.tenant, s.app, m.Chat, id)
				if err != nil {
					return false, err
				}
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
 WHERE x.tenant_key=$1 AND x.app_id=$2 AND x.credentials_generation=$3 AND x.next_at<=now()
 ORDER BY x.next_at,x.file_id LIMIT 1 FOR UPDATE OF s SKIP LOCKED)
 UPDATE feishu_file_shares s SET attempted_at=clock_timestamp(),next_at=now()+interval '2 minutes'
 FROM candidate c WHERE (s.tenant_key,s.app_id,s.message_id,s.file_id)=(c.tenant_key,c.app_id,c.message_id,c.file_id)
 RETURNING c.file_id,c.kind,c.token,c.name,c.chat_id,c.chat_kind,c.message_id,s.attempted_at`, s.tenant, s.app, s.generation).
		Scan(&j.id, &j.Kind, &j.Token, &j.Name, &j.chat, &j.ChatKind, &j.MessageID, &j.attempt)
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var stored int64
	if failure == nil {
		// 中断的流回滚数据块，外层事务仍可记录重试状态。
		err = pgx.BeginFunc(ctx, tx, func(stage pgx.Tx) error {
			var body io.Reader = bytes.NewReader(snap.Content)
			if snap.Stream != nil {
				body = snap.Stream
			}
			var writeErr error
			stored, writeErr = storeVersion(fetch, stage, j.id, body)
			return writeErr
		})
		if errors.Is(err, ErrPartial) {
			failure = err
		} else if err != nil {
			return false, err
		}
	}
	var previous *int64
	err = tx.QueryRow(ctx, `SELECT s.version_id FROM feishu_file_shares s JOIN feishu_file_sources x
 USING(tenant_key,app_id,message_id,file_id) WHERE x.tenant_key=$1 AND x.app_id=$2 AND x.message_id=$3 AND x.file_id=$4
 AND x.attempted_at=$5 AND x.credentials_generation=$6 FOR UPDATE OF s`, s.tenant, s.app, j.MessageID, j.id, j.attempt, s.generation).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	version := previous
	state := "complete"
	delay := "5 minutes"
	switch {
	case failure == nil:
		version = &stored
	case errors.Is(failure, ErrRevoked):
		state = "revoked"
	case errors.Is(failure, ErrPartial):
		state = "partial"
		delay = "1 minute"
	default:
		state = "failed"
		if previous != nil {
			state = "expired"
		}
		delay = "30 seconds"
	}
	_, err = tx.Exec(ctx, `UPDATE feishu_file_shares SET state=$5,version_id=$6,name=coalesce(nullif($7,''),name),
 synced_at=CASE WHEN $5='complete' THEN attempted_at ELSE synced_at END,
 next_at=now()+$8::interval WHERE tenant_key=$1 AND app_id=$2 AND message_id=$3 AND file_id=$4`,
		s.tenant, s.app, j.MessageID, j.id, state, version, strings.ReplaceAll(snap.Name, "\x00", ""), delay)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// 内容版本按序保存数据块摘要，同一文件的相同数据块只存一次。
func storeVersion(ctx context.Context, tx pgx.Tx, file int64, body io.Reader) (int64, error) {
	hash := sha256.New()
	chunks := [][]byte{}
	var size int64
	buf := make([]byte, 1<<20)
	for {
		n := 0
		var err error
		for n < len(buf) && err == nil {
			var count int
			count, err = body.Read(buf[n:])
			n += count
			if count == 0 && err == nil {
				return 0, ErrPartial
			}
		}
		if err != nil && err != io.EOF {
			return 0, ErrPartial
		}
		if n > 0 {
			chunk := buf[:n]
			digest := sha256.Sum256(chunk)
			_, writeErr := tx.Exec(ctx, `INSERT INTO feishu_file_chunks(file_id,digest,content) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, file, digest[:], chunk)
			if writeErr != nil {
				return 0, writeErr
			}
			hash.Write(chunk)
			size += int64(n)
			chunks = append(chunks, digest[:])
		}
		if err == io.EOF {
			break
		}
	}
	sum := hash.Sum(nil)
	_, err := tx.Exec(ctx, `INSERT INTO feishu_file_versions(file_id,digest,chunks,byte_size) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, file, sum, chunks, size)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM feishu_file_versions WHERE file_id=$1 AND digest=$2`, file, sum).Scan(&id)
	return id, err
}

func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		indexed, e := s.index(ctx)
		worked, err := s.refresh(ctx)
		if e != nil || err != nil {
			slog.WarnContext(ctx, "飞书文件同步暂未完成，将自动重试")
		}
		delay := 2 * time.Second
		if (worked || indexed) && err == nil && e == nil {
			delay = 50 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
