package project

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type taskSource struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Chat    string    `json:"chat_id,omitempty"`
	Title   string    `json:"title,omitempty"`
	Text    string    `json:"text"`
	Created time.Time `json:"created_at"`
}
type evidenceItem struct {
	Accessible bool         `json:"accessible"`
	Evidence   []taskSource `json:"evidence,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

func validSource(kind, id string) bool {
	return identifier.MatchString(id) && (kind == "document" || kind == "minutes" || kind == "message")
}

// 文档和纪要保存本地正文。消息只保存引用，读取时核对当前归属、群权限和撤回状态。
func (s *Service) setSource(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		Title   string   `json:"title"`
		Body    string   `json:"body"`
		Chat    string   `json:"chat_id"`
		Readers []string `json:"reader_account_ids"`
	}
	ctx, k, kind, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("kind"), r.PathValue("sourceID")
	if !validSource(kind, id) || !decodeLimit(w, r, &in, 128<<10) || in.Readers == nil || len(in.Readers) > 500 || !textLimit(in.Title, 200) || !textLimit(in.Body, 20000) {
		return nil, errInput
	}
	if kind == "message" {
		if !identifier.MatchString(in.Chat) || in.Title != "" || in.Body != "" {
			return nil, errInput
		}
	} else if in.Chat != "" {
		return nil, errInput
	}
	err := k.write(ctx, s, a, "project.delegate", func(tx pgx.Tx, _ int64) error {
		if kind == "message" {
			src := taskSource{ID: id, Kind: kind, Chat: in.Chat}
			if err := s.messageSource(ctx, tx, k, a, &src); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,title,body,chat_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT(tenant_key,app_id,project_id,kind,id) DO UPDATE SET title=EXCLUDED.title,body=EXCLUDED.body,chat_id=EXCLUDED.chat_id`, k.args(kind, id, in.Title, in.Body, in.Chat)...)
		if err != nil {
			return err
		}
		return k.sourceReaders(ctx, tx, kind, id, in.Readers)
	})
	return map[string]string{"id": id}, err
}
func (k taskScope) sourceReaders(ctx context.Context, tx pgx.Tx, kind, id string, readers []string) error {
	_, err := tx.Exec(ctx, "DELETE FROM project_source_readers WHERE "+projectWhere+" AND kind=$4 AND source_id=$5", k.args(kind, id)...)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO project_source_readers SELECT m.tenant_key,m.app_id,m.project_id,$4,$5,m.account_id FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.account_id=ANY($6) AND m.role!='revoked' AND a.active`, k.args(kind, id, readers)...)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(readers) {
		return errInput
	}
	return nil
}
func (s *Service) setSourceReaders(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, kind, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("kind"), r.PathValue("sourceID")
	var in struct {
		Readers []string `json:"reader_account_ids"`
	}
	if !validSource(kind, id) || !decodeLimit(w, r, &in, 64<<10) || in.Readers == nil || len(in.Readers) > 500 {
		return nil, errInput
	}
	err := k.write(ctx, s, a, "project.delegate", func(tx pgx.Tx, _ int64) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_sources WHERE "+projectWhere+" AND kind=$4 AND id=$5)", k.args(kind, id)...).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errDenied
		}
		return k.sourceReaders(ctx, tx, kind, id, in.Readers)
	})
	return map[string]string{"id": id}, err
}
func (s *Service) messageSource(ctx context.Context, tx pgx.Tx, k taskScope, a identity.Principal, src *taskSource) error {
	if s.chat == nil {
		return errUnavailable
	}
	if _, err := s.chat.ProjectGroupForMutation(ctx, tx, a, src.Chat); err != nil {
		return err
	}
	err := tx.QueryRow(ctx, `SELECT m.body,m.sent_at FROM feishu_chat_messages m JOIN project_chat_assignments g USING(tenant_key,app_id,chat_id)
 WHERE g.tenant_key=$1 AND g.app_id=$2 AND g.project_id=$3 AND m.message_id=$4 AND m.chat_id=$5 AND m.visible_in_chat AND NOT m.deleted
 AND NOT EXISTS(SELECT FROM feishu_candidate_event_owners o
    WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL)`, k.args(src.ID, src.Chat)...).Scan(&src.Text, &src.Created)
	return taskError(err)
}
func (s *Service) source(ctx context.Context, tx pgx.Tx, k taskScope, a identity.Principal, kind, id string) (taskSource, error) {
	src := taskSource{ID: id, Kind: kind}
	err := tx.QueryRow(ctx, `SELECT s.title,s.body,s.chat_id,s.created_at FROM project_sources s
 JOIN project_source_readers r ON (r.tenant_key,r.app_id,r.project_id,r.kind,r.source_id)=(s.tenant_key,s.app_id,s.project_id,s.kind,s.id)
 JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=(r.tenant_key,r.app_id,r.project_id,r.account_id)
 WHERE s.tenant_key=$1 AND s.app_id=$2 AND s.project_id=$3 AND s.kind=$4 AND s.id=$5 AND r.account_id=$6 AND m.role!='revoked'`, k.args(kind, id, a.AccountID)...).Scan(&src.Title, &src.Text, &src.Chat, &src.Created)
	if err != nil {
		return taskSource{}, taskError(err)
	}
	if kind == "message" {
		err = s.messageSource(ctx, tx, k, a, &src)
	}
	return src, err
}

func (s *Service) linkEvidence(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, kind, id, task := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("kind"), r.PathValue("sourceID"), r.PathValue("taskID")
	if !validSource(kind, id) || !identifier.MatchString(task) {
		return nil, errInput
	}
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		if _, err := s.source(ctx, tx, k, a, kind, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_task_sources VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, k.args(task, kind, id)...)
		return err
	})
	return map[string]string{"task_id": task}, err
}

func (s *Service) evidence(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID")
	items := []evidenceItem{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := k.task(ctx, tx, id, time.Now()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT kind,source_id FROM project_task_sources WHERE "+projectWhere+" AND task_id=$4 ORDER BY kind,source_id", k.args(id)...)
		if err != nil {
			return err
		}
		type ref struct{ Kind, ID string }
		refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ref])
		if err != nil {
			return err
		}
		for _, ref := range refs {
			src, err := s.source(ctx, tx, k, a, ref.Kind, ref.ID)
			if errors.Is(err, errDenied) || errors.Is(err, chat.ErrAccessLost) {
				items = append(items, evidenceItem{Reason: "当前账号无权读取原始依据。"})
				continue
			}
			if err != nil {
				return err
			}
			items = append(items, evidenceItem{Accessible: true, Evidence: []taskSource{src}})
		}
		return nil
	})
	return map[string]any{"items": items}, err
}
func (s *Service) localSourceTasks(r *http.Request, a identity.Principal) (any, error) {
	ctx, k, kind, id := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("kind"), r.PathValue("sourceID")
	if !validSource(kind, id) {
		return nil, errInput
	}
	items := []storedTask{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		src, err := s.source(ctx, tx, k, a, kind, id)
		if err != nil {
			return err
		}
		if kind == "message" && src.Chat != r.URL.Query().Get("chat_id") {
			return errDenied
		}
		rows, err := tx.Query(ctx, "SELECT task_id FROM project_task_sources WHERE "+projectWhere+" AND kind=$4 AND source_id=$5 ORDER BY task_id", k.args(kind, id)...)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			t, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return err
			}
			items = append(items, t)
		}
		return nil
	})
	return map[string]any{"items": items, "total": len(items)}, err
}
