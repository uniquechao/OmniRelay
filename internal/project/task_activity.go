package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var errRequestKey = errors.New("请求键已用于其他任务命令")

// 写入已持有项目看板锁。先校验当前动作权限，再重放同一账号的回执。
func (k taskScope) command(ctx context.Context, tx pgx.Tx, actor identity.Principal, key string, payload any, apply func() (any, error)) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if key != "" {
		var same bool
		err = tx.QueryRow(ctx, "SELECT command=$6::jsonb,result FROM project_task_moves WHERE "+projectWhere+" AND account_id=$4 AND request_key=$5", k.args(actor.AccountID, key, raw)...).Scan(&same, &result)
		if err == nil {
			if !same {
				return nil, errRequestKey
			}
			return result, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	value, err := apply()
	if err != nil {
		return nil, err
	}
	result, err = json.Marshal(value)
	if err != nil || key == "" {
		return result, err
	}
	err = tx.QueryRow(ctx, "INSERT INTO project_task_moves(tenant_key,app_id,project_id,account_id,request_key,command,result) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING result", k.args(actor.AccountID, key, raw, result)...).Scan(&result)
	return result, err
}

func validKey(key string) bool               { return len(key) <= 100 && strings.TrimSpace(key) != "" }
func textLimit(value string, limit int) bool { return len([]rune(value)) <= limit }

type activityEvent struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Action  string          `json:"action"`
	Actor   string          `json:"actor_id"`
	Name    string          `json:"actor_name"`
	Created time.Time       `json:"created_at"`
	Body    string          `json:"body"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
}

func (k taskScope) event(ctx context.Context, tx pgx.Tx, actor identity.Principal, id, kind, action, body string, before, after map[string]any) (activityEvent, error) {
	var out activityEvent
	err := tx.QueryRow(ctx, `INSERT INTO project_task_activity(tenant_key,app_id,project_id,task_id,kind,action,actor_id,actor_name,body,before_values,after_values)
 SELECT $1,$2,$3,$4,$5,$6,id,COALESCE(NULLIF(name,''),id),$8,$9,$10 FROM accounts WHERE id=$7 AND tenant_key=$1 AND app_id=$2
 RETURNING id,kind,action,actor_id,actor_name,created_at,body,before_values,after_values`, k.args(id, kind, action, actor.AccountID, body, before, after)...).Scan(&out.ID, &out.Kind, &out.Action, &out.Actor, &out.Name, &out.Created, &out.Body, &out.Before, &out.After)
	return out, err
}

func taskValues(t storedTask) map[string]any {
	if t.ID == "" {
		return map[string]any{}
	}
	status := columnNames[t.Status]
	if t.Status == "cancelled" {
		status = "已取消"
	}
	return map[string]any{"所在列": t.Column, "列内位置": t.Position, "标题": t.Title, "正文": t.Description, "主负责人": t.Owner, "参与人": t.Participants, "验收条件": t.Acceptance, "开始日期": t.Start, "截止日期": t.Due, "优先级": t.PriorityLabel, "标签": t.Tags, "里程碑": t.Milestone, "状态": status, "受阻原因": t.Blocked, "完成结果": t.Completion, "变更原因": t.Change}
}

func (k taskScope) history(ctx context.Context, tx pgx.Tx, actor identity.Principal, action string, old, new storedTask) error {
	before, after := taskValues(old), taskValues(new)
	for key, v := range after {
		if reflect.DeepEqual(v, before[key]) {
			delete(before, key)
			delete(after, key)
		}
	}
	// 显示名称随事件冻结，保留稳定标识以区分同名关系。
	var names map[string]string
	err := tx.QueryRow(ctx, "SELECT relation_names FROM project_tasks WHERE "+projectWhere+" AND id=$4", k.args(new.ID)...).Scan(&names)
	if err != nil {
		return err
	}
	for _, values := range []map[string]any{before, after} {
		for _, field := range []string{"主负责人", "参与人", "标签", "里程碑"} {
			label := func(id string) string {
				if name := names[id]; name != "" {
					return name + "（" + id + "）"
				}
				return id
			}
			switch x := values[field].(type) {
			case *string:
				if x != nil {
					values[field] = label(*x)
				}
			case []string:
				labels := make([]string, len(x))
				for i, id := range x {
					labels[i] = label(id)
				}
				values[field] = labels
			}
		}
	}
	_, err = k.event(ctx, tx, actor, new.ID, "change", action, "", before, after)
	return err
}

func (s *Service) comment(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in progressInput
	if !decodeLimit(w, r, &in, 32<<10) {
		return nil, errInput
	}
	return s.addProgress(r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID"), a, in)
}

type progressInput struct {
	Version int64  `json:"expected_version"`
	Key     string `json:"request_key"`
	Body    string `json:"body"`
}

func (s *Service) addProgress(ctx context.Context, k taskScope, id string, a identity.Principal, in progressInput) (any, error) {
	if in.Version < 1 || !validKey(in.Key) || strings.TrimSpace(in.Body) == "" || !textLimit(in.Body, 4000) {
		return nil, errInput
	}
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.comment", func(tx pgx.Tx, _ int64) error {
		var err error
		result, err = k.command(ctx, tx, a, in.Key, []any{"comment", id, in}, func() (any, error) {
			task, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			if task.Version != in.Version {
				return nil, errTaskVersion
			}
			return k.event(ctx, tx, a, id, "comment", "task.commented", strings.TrimSpace(in.Body), map[string]any{}, map[string]any{})
		})
		return err
	})
	return result, err
}

func (s *Service) activity(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k, id, ctx := scope(a, r.PathValue("projectID")), r.PathValue("taskID"), r.Context()
	if _, err := k.task(ctx, s.pool, id, time.Now()); err != nil {
		return nil, err
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "all" {
		kind = ""
	}
	if kind != "" && kind != "change" && kind != "comment" {
		return nil, errInput
	}
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			return nil, errInput
		}
		limit = n
	}
	var stamp *time.Time
	var seq int64
	if q.Has("cursor") {
		date, key, ok := strings.Cut(q.Get("cursor"), "|")
		t, e := time.Parse(time.RFC3339Nano, date)
		n, f := strconv.ParseInt(key, 10, 64)
		if !ok || e != nil || f != nil || n < 1 {
			return nil, errInput
		}
		stamp = &t
		seq = n
	}
	rows, err := s.pool.Query(ctx, "SELECT id,kind,action,actor_id,actor_name,created_at,body,before_values,after_values FROM project_task_activity WHERE "+projectWhere+" AND task_id=$4 AND ($5='' OR kind=$5) AND ($6::timestamptz IS NULL OR (created_at,id)<($6,$7)) ORDER BY created_at DESC,id DESC LIMIT $8", k.args(id, kind, stamp, seq, limit+1)...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[activityEvent])
	if err != nil {
		return nil, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		cursor := last.Created.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(last.ID, 10)
		next = &cursor
	}
	return map[string]any{"items": items, "next_cursor": next}, nil
}
