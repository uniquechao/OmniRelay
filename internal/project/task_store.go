package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type taskScope struct{ tenant, app, project string }

func scope(actor identity.Principal, id string) taskScope {
	return taskScope{actor.TenantKey, actor.AppID, id}
}
func (k taskScope) args(extra ...any) []any {
	return append([]any{k.tenant, k.app, k.project}, extra...)
}

const projectWhere = "tenant_key=$1 AND app_id=$2 AND project_id=$3"

type taskFields struct {
	Participants []string `json:"participant_ids"`
	Acceptance   string   `json:"acceptance"`
	Start        *string  `json:"start_at"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Owner        *string  `json:"owner_member_id"`
	Milestone    *string  `json:"milestone_id"`
	Priority     *string  `json:"priority"`
	Due          *string  `json:"due_at"`
	Tags         []string `json:"tag_ids"`
}
type storedTask struct {
	CreatedBy     *string    `json:"created_by"`
	CreatedByName *string    `json:"created_by_name"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedBy     *string    `json:"updated_by"`
	UpdatedByName *string    `json:"updated_by_name"`
	UpdatedAt     *time.Time `json:"updated_at"`
	CanMove       *bool      `json:"can_move,omitempty"`
	taskFields
	ID            string `json:"id"`
	Project       string `json:"project_id"`
	Version       int64  `json:"version"`
	BoardVersion  int64  `json:"board_version"`
	Position      int64  `json:"position"`
	Column        string `json:"column_id"`
	Status        string `json:"status"`
	Blocked       string `json:"blocked_reason"`
	Completion    string `json:"completion_note"`
	Change        string `json:"change_reason"`
	PriorityLabel string `json:"priority_label"`
	DueLabel      string `json:"due_label"`
	DueAlertLabel string `json:"due_alert_label,omitempty"`
	DueAlert      bool   `json:"due_alert"`
}

var priorityLabels = map[string]string{"P0": "紧急", "P1": "重要", "P2": "普通", "P3": "低"}
var columnOrder = []string{"todo", "in_progress", "blocked", "done"}
var columnNames = map[string]string{"todo": "待开始", "in_progress": "进行中", "blocked": "受阻", "done": "已完成"}

func businessDate(raw string, loc *time.Location) (time.Time, error) {
	if d, err := time.ParseInLocation(time.DateOnly, raw, loc); err == nil {
		return d, nil
	}
	instant, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errInput
	}
	y, m, d := instant.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc), nil
}

func (t *storedTask) present(now time.Time, version int64) error {
	t.BoardVersion = version
	if t.Priority != nil {
		t.PriorityLabel = priorityLabels[*t.Priority]
		if t.PriorityLabel == "" {
			t.PriorityLabel = *t.Priority
		}
	}
	t.DueLabel, t.DueAlertLabel, t.DueAlert = "", "", false
	if t.Due == nil {
		return nil
	}
	due, err := businessDate(*t.Due, now.Location())
	if err != nil {
		return err
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	t.DueLabel = due.Format("1/2")
	if due.Year() != y {
		t.DueLabel = due.Format("2006/1/2")
	}
	t.DueAlert = t.Status != "done" && t.Status != "cancelled" && !due.After(today.AddDate(0, 0, 3))
	if t.DueAlert {
		switch {
		case due.Before(today):
			t.DueAlertLabel = "已逾期"
		case due.Equal(today):
			t.DueAlertLabel = "今日到期"
		default:
			t.DueAlertLabel = "临期"
		}
	}
	return nil
}
func taskError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errDenied
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23503" || pgErr.Code == "23514" || pgErr.Code == "22007" || pgErr.Code == "22008") {
		return errInput
	}
	return err
}

// 所有写命令先锁定同一项目的看板，回执与双版本在同一事务提交。
func (k taskScope) write(ctx context.Context, s *Service, actor identity.Principal, action string, fn func(pgx.Tx, int64) error) error {
	return taskError(s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, action, k.project)
		if err != nil {
			return err
		}
		if !allowed {
			return errDenied
		}
		if _, err := tx.Exec(ctx, "INSERT INTO project_task_boards(tenant_key,app_id,project_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", k.args()...); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRow(ctx, "SELECT version FROM project_task_boards WHERE "+projectWhere+" FOR UPDATE", k.args()...).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('omnirelay.task_actor',$1,true)", actor.AccountID); err != nil {
			return err
		}
		return fn(tx, version)
	}))
}
func (k taskScope) bump(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "UPDATE project_task_boards SET version=version+1 WHERE "+projectWhere, k.args()...)
	return err
}

type taskReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (k taskScope) task(ctx context.Context, db taskReader, id string, now time.Time) (storedTask, error) {
	var result storedTask
	var raw []byte
	var version int64
	var zone string
	err := db.QueryRow(ctx, "SELECT to_jsonb(t), b.version,b.business_timezone FROM project_tasks t JOIN project_task_boards b USING(tenant_key,app_id,project_id) WHERE "+projectWhere+" AND t.id=$4", k.args(id)...).Scan(&raw, &version, &zone)
	if err != nil {
		return result, taskError(err)
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return result, err
	}
	err = result.present(now.In(loc), version)
	return result, err
}

type boardColumn struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Items  []storedTask `json:"items"`
	Total  int          `json:"total"`
	Cursor *string      `json:"next_cursor"`
}
type taskBoard struct {
	CanMove  bool          `json:"can_move"`
	Owners   []string      `json:"owner_member_ids"`
	Project  string        `json:"project_id"`
	Version  int64         `json:"version"`
	Timezone string        `json:"business_timezone"`
	Columns  []boardColumn `json:"columns"`
	Total    int           `json:"total"`
}

func (k taskScope) board(ctx context.Context, pool *pgxpool.Pool, query url.Values, now time.Time) (taskBoard, error) {
	board := taskBoard{Project: k.project}
	limit, offset := 50, 0
	selected := query.Get("column_id")
	if selected != "" && columnNames[selected] == "" {
		return board, errInput
	}
	if query.Has("limit") {
		n, e := strconv.Atoi(query.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return board, errInput
		}
		limit = n
	}
	var cursorVersion int64
	if query.Has("cursor") {
		parts := strings.Split(query.Get("cursor"), ".")
		if len(parts) != 2 || selected == "" {
			return board, errInput
		}
		var err error
		cursorVersion, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || cursorVersion < 1 {
			return board, errInput
		}
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset > 1<<30 {
			return board, errInput
		}
	}
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT version,business_timezone FROM project_task_boards WHERE "+projectWhere, k.args()...).Scan(&board.Version, &board.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			board.Version, board.Timezone = 1, "Asia/Shanghai"
		} else if err != nil {
			return err
		}
		loc, err := time.LoadLocation(board.Timezone)
		if err != nil {
			return err
		}
		now = now.In(loc)
		if milestone := query.Get("milestone_id"); milestone != "" {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM project_milestones WHERE "+projectWhere+" AND id=$4)", k.args(milestone)...).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errInput
			}
		}
		if cursorVersion != 0 && cursorVersion != board.Version {
			return errConflict
		}
		rows, err := tx.Query(ctx, "SELECT DISTINCT owner_member_id FROM project_tasks WHERE "+projectWhere+" AND owner_member_id IS NOT NULL ORDER BY owner_member_id", k.args()...)
		if err != nil {
			return err
		}
		board.Owners, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range columnOrder {
			if selected != "" && selected != id {
				continue
			}
			c := boardColumn{ID: id, Name: columnNames[id], Items: []storedTask{}}
			filter := " FROM project_tasks t WHERE " + projectWhere + ` AND column_id=$4 AND ($5='' OR strpos(lower(title||' '||description),lower($5))>0) AND ($6='' OR owner_member_id=$6) AND ($7='' OR milestone_id=$7) AND (NOT $8 OR milestone_id IS NULL) AND ($9='' OR status=$9)`
			args := k.args(id, query.Get("q"), query.Get("owner_member_id"), query.Get("milestone_id"), query.Get("milestone_unassigned") == "true", query.Get("status"))
			if err := tx.QueryRow(ctx, "SELECT count(*)"+filter, args...).Scan(&c.Total); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "SELECT to_jsonb(t)"+filter+" ORDER BY position,id LIMIT $10 OFFSET $11", append(args, limit, offset)...)
			if err != nil {
				return err
			}
			tasks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedTask, error) {
				var task storedTask
				if err := r.Scan(&task); err != nil {
					return task, err
				}
				err := task.present(now, board.Version)
				return task, err
			})
			if err != nil {
				return err
			}
			c.Items = tasks
			if offset+len(tasks) < c.Total {
				cursor := fmt.Sprintf("%d.%d", board.Version, offset+len(tasks))
				c.Cursor = &cursor
			}
			board.Columns = append(board.Columns, c)
			board.Total += c.Total
		}
		return nil
	})
	return board, err
}
