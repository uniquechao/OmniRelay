package project

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type milestoneInput struct {
	Title string  `json:"title"`
	Date  *string `json:"occurs_on"`
	Task  *string `json:"task_id"`
}

func (s *Service) createMilestone(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in milestoneInput
	if !decode(w, r, &in) || strings.TrimSpace(in.Title) == "" {
		return nil, errInput
	}
	if in.Date != nil {
		if _, err := time.Parse(time.DateOnly, *in.Date); err != nil {
			return nil, errInput
		}
	}
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	var id string
	err := k.write(ctx, s, actor, "project.assign", func(tx pgx.Tx, v int64) error {
		if err := tx.QueryRow(ctx, "INSERT INTO project_milestones(tenant_key,app_id,project_id,title,occurs_on,task_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", k.args(in.Title, in.Date, in.Task)...).Scan(&id); err != nil {
			return err
		}
		return k.bump(ctx, tx)
	})
	return map[string]any{"id": id, "project_id": k.project, "title": in.Title, "occurs_on": in.Date, "task_id": in.Task, "kind": "milestone"}, err
}

func (s *Service) milestones(ctx context.Context, k taskScope, now time.Time) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id,m.title,m.occurs_on::text,m.task_id,to_jsonb(t),b.version,b.business_timezone
 FROM project_milestones m JOIN project_task_boards b USING(tenant_key,app_id,project_id)
 LEFT JOIN project_tasks t ON (t.tenant_key,t.app_id,t.project_id,t.id)=(m.tenant_key,m.app_id,m.project_id,m.task_id)
 WHERE (m.tenant_key,m.app_id,m.project_id)=($1,$2,$3) ORDER BY m.occurs_on NULLS LAST,m.id`, k.args()...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (map[string]any, error) {
		var id, title, zone string
		var date, taskID *string
		var raw []byte
		var version int64
		if err := row.Scan(&id, &title, &date, &taskID, &raw, &version, &zone); err != nil {
			return nil, err
		}
		item := map[string]any{"kind": "milestone", "project_id": k.project, "id": id, "title": title, "occurs_on": date, "task_id": taskID}
		if taskID != nil {
			var task storedTask
			if err := json.Unmarshal(raw, &task); err != nil {
				return nil, err
			}
			loc, err := time.LoadLocation(zone)
			if err != nil {
				return nil, err
			}
			if err := task.present(now.In(loc), version); err != nil {
				return nil, err
			}
			item["task"], item["owner_member_id"], item["status"] = task, task.Owner, task.Status
		}
		return item, nil
	})
	return items, err
}

func (s *Service) setBoardTimezone(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Timezone string `json:"business_timezone"`
		Expected int64  `json:"expected_board_version"`
	}
	if !decode(w, r, &in) || in.Timezone == "" || in.Timezone == "Local" || in.Expected < 1 {
		return nil, errInput
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return nil, errInput
	}
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	err := k.write(ctx, s, actor, "project.settings", func(tx pgx.Tx, v int64) error {
		if v != in.Expected {
			return errConflict
		}
		_, err := tx.Exec(ctx, "UPDATE project_task_boards SET business_timezone=$4,version=version+1 WHERE "+projectWhere, k.args(in.Timezone)...)
		return err
	})
	return map[string]any{"version": in.Expected + 1, "business_timezone": in.Timezone}, err
}

// 单独调整归属时只更新任务侧关联，节点绑定和其他任务属性独立保留。
func (s *Service) setTaskMilestone(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Expected  int64           `json:"expected_task_version"`
		Milestone json.RawMessage `json:"milestone_id"`
	}
	var milestone *string
	if !decode(w, r, &in) || in.Expected < 1 || len(in.Milestone) == 0 || json.Unmarshal(in.Milestone, &milestone) != nil || milestone != nil && !identifier.MatchString(*milestone) {
		return nil, errInput
	}
	k, ctx, id := scope(actor, r.PathValue("projectID")), r.Context(), r.PathValue("taskID")
	var task storedTask
	err := k.write(ctx, s, actor, "project.assign", func(tx pgx.Tx, v int64) error {
		var err error
		task, err = k.task(ctx, tx, id, time.Now())
		if err != nil {
			return err
		}
		if task.Version != in.Expected {
			return errTaskVersion
		}
		old := task
		names, err := k.relationNames(ctx, tx, taskFields{Milestone: milestone})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE project_tasks SET milestone_id=$5,relation_names=relation_names||$6::jsonb,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, milestone, names)...); err != nil {
			return err
		}
		if err = k.bump(ctx, tx); err != nil {
			return err
		}
		task, err = k.task(ctx, tx, id, time.Now())
		if err != nil {
			return err
		}
		return k.history(ctx, tx, actor, "task.milestone_updated", old, task)
	})
	return task, err
}
