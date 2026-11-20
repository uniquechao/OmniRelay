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

type saveTask struct {
	taskFields
	Expected int64  `json:"expected_task_version"`
	Status   string `json:"status,omitempty"`
}

func (s *Service) saveTask(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in saveTask
	if !decodeLimit(w, r, &in, 96<<10) || strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 200 || len(in.Tags) > 50 {
		return nil, errInput
	}
	if in.Owner != nil && !identifier.MatchString(*in.Owner) || in.Milestone != nil && !identifier.MatchString(*in.Milestone) {
		return nil, errInput
	}
	if in.Priority != nil && priorityLabels[*in.Priority] == "" {
		return nil, errInput
	}
	if in.Due != nil {
		if _, err := businessDate(*in.Due, time.UTC); err != nil {
			return nil, err
		}
	}
	if in.Status != "" && in.Status != "cancelled" && columnNames[in.Status] == "" {
		return nil, errInput
	}
	for _, id := range in.Tags {
		if !identifier.MatchString(id) {
			return nil, errInput
		}
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	id := r.PathValue("taskID")
	if id != "" && (!identifier.MatchString(id) || in.Expected < 1) {
		return nil, errInput
	}
	k := scope(actor, r.PathValue("projectID"))
	ctx := r.Context()
	var result storedTask
	err := k.write(ctx, s, actor, "project.assign", func(tx pgx.Tx, v int64) error {
		args := k.args(in.Title, in.Description, in.Owner, in.Milestone, in.Priority, in.Due, in.Tags)
		if id == "" {
			if in.Expected != 0 {
				return errInput
			}
			status := in.Status
			if status == "" {
				status = "todo"
			}
			column := status
			if status == "cancelled" {
				column = "todo"
			}
			err := tx.QueryRow(ctx, `INSERT INTO project_tasks(tenant_key,app_id,project_id,title,description,owner_member_id,milestone_id,priority,due_at,tag_ids,status,column_id,position)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,COALESCE(max(position),0)+1 FROM project_tasks WHERE `+projectWhere+" AND column_id=$12 RETURNING id", append(args, status, column)...).Scan(&id)
			if err != nil {
				return err
			}
		} else {
			// 看板列通过移动命令修改，属性保存仅允许取消或保留原状态。
			current, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return err
			}
			if current.Version != in.Expected {
				return errTaskVersion
			}
			if in.Status != "" && in.Status != "cancelled" && in.Status != current.Status {
				return errInput
			}
			_, err = tx.Exec(ctx, `UPDATE project_tasks SET title=$4,description=$5,owner_member_id=$6,milestone_id=$7,priority=$8,due_at=$9,tag_ids=$10,status=COALESCE(NULLIF($11,''),status),version=version+1 WHERE `+projectWhere+" AND id=$12", append(args, in.Status, id)...)
			if err != nil {
				return err
			}
		}
		if err := k.bump(ctx, tx); err != nil {
			return err
		}
		var err error
		result, err = k.task(ctx, tx, id, time.Now())
		return err
	})
	return result, err
}

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
		if _, err = tx.Exec(ctx, "UPDATE project_tasks SET milestone_id=$5,version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(id, milestone)...); err != nil {
			return err
		}
		if err = k.bump(ctx, tx); err != nil {
			return err
		}
		task, err = k.task(ctx, tx, id, time.Now())
		return err
	})
	return task, err
}
