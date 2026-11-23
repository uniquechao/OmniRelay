package project

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type saveTask struct {
	AddTags         []string `json:"add_tags,omitempty"`
	RemoveTags      []string `json:"remove_tags,omitempty"`
	ExpectedVersion *int64   `json:"expected_version,omitempty"`
	Column          string   `json:"column_id,omitempty"`
	BoardVersion    *int64   `json:"expected_board_version,omitempty"`
	taskFields
	Expected   int64   `json:"expected_task_version"`
	Key        string  `json:"request_key"`
	Status     string  `json:"status"`
	Blocked    *string `json:"blocked_reason"`
	Completion *string `json:"completion_note"`
	Change     string  `json:"change_reason"`
}

func transition(from, to, blocked, reason string, manage bool) error {
	if to != "cancelled" && columnNames[to] == "" || to == "blocked" && strings.TrimSpace(blocked) == "" {
		return errInput
	}
	if from != to && (to == "cancelled" || from == "done" || from == "cancelled") {
		if !manage {
			return errDenied
		}
		if strings.TrimSpace(reason) == "" {
			return errInput
		}
	}
	return nil
}

func ownTask(task storedTask, member string) bool {
	return member != "" && (task.Owner != nil && *task.Owner == member || slices.Contains(task.Participants, member))
}

func canUpdateProgress(t storedTask, g access.ProjectAccess) bool {
	return g.Member && (g.CanAssign || g.CanUpdateOwn && ownTask(t, g.MemberID) && t.Status != "cancelled" && t.Status != "done")
}

// 成员、标签由当前项目目录确认；里程碑由本地复合外键约束。
func (k taskScope) relationNames(ctx context.Context, tx pgx.Tx, f taskFields, previous ...storedTask) (map[string]string, error) {
	names := map[string]string{}
	if f.Milestone != nil {
		var name string
		if err := tx.QueryRow(ctx, "SELECT title FROM project_milestones WHERE "+projectWhere+" AND id=$4", k.args(*f.Milestone)...).Scan(&name); err == pgx.ErrNoRows {
			return nil, errInput
		} else if err != nil {
			return nil, err
		}
		names[*f.Milestone] = name
	}
	members := slices.Clone(f.Participants)
	if f.Owner != nil {
		members = append(members, *f.Owner)
	}
	for _, id := range members {
		var name string
		err := tx.QueryRow(ctx, `SELECT coalesce(nullif(a.name,''),m.member_id) FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.member_id=$4 AND a.active AND m.role!='revoked'`, k.args(id)...).Scan(&name)
		if err == pgx.ErrNoRows && strings.HasPrefix(id, "feishu:") {
			err = tx.QueryRow(ctx, groupPeople+` SELECT display_name FROM people WHERE id=$4 AND role IS NULL`, k.args(id)...).Scan(&name)
			if err == pgx.ErrNoRows && len(previous) > 0 && (f.Owner == nil || *f.Owner != id || previous[0].Owner != nil && *previous[0].Owner == id) && (!slices.Contains(f.Participants, id) || slices.Contains(previous[0].Participants, id)) && previous[0].RelationNames[id] != "" {
				name, err = previous[0].RelationNames[id], nil
			}
		}
		if err == pgx.ErrNoRows {
			return nil, errInput
		}
		if err != nil {
			return nil, err
		}
		names[id] = name
	}
	for _, id := range f.Tags {
		var name string
		err := tx.QueryRow(ctx, "SELECT name FROM project_task_tags WHERE "+projectWhere+" AND id=$4", k.args(id)...).Scan(&name)
		if err == pgx.ErrNoRows {
			return nil, errInput
		}
		if err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, nil
}

func (s *Service) saveTask(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in saveTask
	if !decodeLimit(w, r, &in, 96<<10) {
		return nil, errInput
	}
	return s.saveTaskInput(r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("taskID"), a, in)
}
func (s *Service) saveTaskInput(ctx context.Context, k taskScope, id string, a identity.Principal, in saveTask) (any, error) {
	if in.ExpectedVersion != nil {
		if in.Expected != 0 && in.Expected != *in.ExpectedVersion {
			return nil, errInput
		}
		in.Expected = *in.ExpectedVersion
	}
	incremental := in.AddTags != nil || in.RemoveTags != nil
	if incremental && (in.Tags != nil || id == "") {
		return nil, errInput
	}
	if strings.TrimSpace(in.Title) == "" || !textLimit(in.Title, 200) || !textLimit(in.Description, 10000) || !textLimit(in.Acceptance, 4000) || !textLimit(in.Change, 4000) || in.Key != "" && !validKey(in.Key) {
		return nil, errInput
	}
	for _, field := range []*string{in.Blocked, in.Completion} {
		if field != nil && !textLimit(*field, 4000) {
			return nil, errInput
		}
	}
	for _, ids := range [][]string{in.Participants, in.Tags} {
		if len(ids) > 50 {
			return nil, errInput
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !identifier.MatchString(id) || seen[id] {
				return nil, errInput
			}
			seen[id] = true
		}
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if in.Participants == nil {
		in.Participants = []string{}
	}
	if in.Owner != nil && !identifier.MatchString(*in.Owner) || in.Milestone != nil && !identifier.MatchString(*in.Milestone) || in.Priority != nil && priorityLabels[*in.Priority] == "" {
		return nil, errInput
	}
	for _, date := range []*string{in.Start, in.Due} {
		if date != nil {
			if _, err := businessDate(*date, time.UTC); err != nil {
				return nil, errInput
			}
		}
	}
	if id == "" && in.Expected != 0 || id != "" && (!identifier.MatchString(id) || in.Expected < 1) {
		return nil, errInput
	}
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.read", func(tx pgx.Tx, boardVersion int64) error {
		grant, err := s.roles.ProjectGrant(ctx, tx, a, k.project)
		if err != nil {
			return err
		}
		var old storedTask
		if id != "" {
			old, err = k.task(ctx, tx, id, time.Now())
			if err != nil {
				return err
			}
		}
		if !grant.Member {
			return errDenied
		}
		if !grant.CanAssign {
			if incremental {
				return errDenied
			}
			if !grant.CanUpdateOwn || in.Change != "" {
				return errDenied
			}
			if id == "" {
				if in.Owner == nil {
					in.Owner = &grant.MemberID
				}
				if *in.Owner != grant.MemberID || len(in.Participants) > 0 || len(in.Tags) > 0 || in.Milestone != nil || in.Priority != nil || in.Start != nil || in.Due != nil || in.Acceptance != "" || (in.Status != "" && in.Status != "todo") {
					return errDenied
				}
			} else {
				permitted := old.taskFields
				permitted.Title, permitted.Description = in.Title, in.Description
				if !ownTask(old, grant.MemberID) || !reflect.DeepEqual(permitted, in.taskFields) {
					return errDenied
				}
			}
		}
		result, err = k.command(ctx, tx, a, in.Key, []any{"save", id, in}, func() (any, error) {
			if incremental {
				var e error
				in.Tags, e = deltaTags(old.Tags, in.AddTags, in.RemoveTags)
				if e != nil {
					return nil, e
				}
			}
			if id != "" && !canUpdateProgress(old, grant) {
				return nil, errDenied
			}
			if in.Start != nil && in.Due != nil {
				var zone string
				if err := tx.QueryRow(ctx, "SELECT business_timezone FROM project_task_boards WHERE "+projectWhere, k.args()...).Scan(&zone); err != nil {
					return nil, err
				}
				loc, err := time.LoadLocation(zone)
				if err != nil {
					return nil, err
				}
				start, _ := businessDate(*in.Start, loc)
				due, _ := businessDate(*in.Due, loc)
				startInstant, se := time.Parse(time.RFC3339, *in.Start)
				dueInstant, de := time.Parse(time.RFC3339, *in.Due)
				if due.Before(start) || se == nil && de == nil && dueInstant.Before(startInstant) {
					return nil, errInput
				}
			}
			if in.BoardVersion != nil && *in.BoardVersion != boardVersion {
				return nil, errBoardVersion
			}
			if old.Version != in.Expected {
				return nil, errTaskVersion
			}
			cfg, err := k.config(ctx, tx)
			if err != nil {
				return nil, err
			}
			status, column := in.Status, in.Column
			if status == "cancelled" {
				column = old.Column
				if column == "" {
					return nil, errInput
				}
			} else {
				if column == "" && old.ID != "" && (status == "" || status == old.Status) {
					column = old.Column
				}
				l, err := cfg.resolve(column, status)
				if err != nil || status != "" && status != l.Stage {
					return nil, errInput
				}
				column, status = l.ID, l.Stage
				if !grant.CanAssign && id == "" && status != "todo" {
					return nil, errDenied
				}
			}
			blocked, completion := old.Blocked, old.Completion
			if in.Blocked != nil {
				blocked = *in.Blocked
			}
			if in.Completion != nil {
				completion = *in.Completion
			}
			if in.Completion != nil && *in.Completion != "" && status != "done" && old.Completion == "" {
				return nil, errInput
			}
			if in.Blocked != nil && strings.TrimSpace(*in.Blocked) != "" && status != "blocked" {
				return nil, errInput
			}
			if err := transition(old.Status, status, blocked, in.Change, grant.CanAssign); err != nil {
				return nil, err
			}
			if status != "blocked" {
				blocked = ""
			}
			names, err := k.relationNames(ctx, tx, in.taskFields, old)
			if err != nil {
				return nil, err
			}
			args := k.args(in.Title, in.Description, in.Owner, in.Milestone, in.Priority, in.Due, in.Tags, status, column, blocked, completion, in.Change, in.Acceptance, in.Participants, in.Start, names)
			action := "task.updated"
			if id == "" {
				action = "task.created"
				err = tx.QueryRow(ctx, `INSERT INTO project_tasks(tenant_key,app_id,project_id,title,description,owner_member_id,milestone_id,priority,due_at,tag_ids,status,column_id,blocked_reason,completion_note,change_reason,acceptance,participant_ids,start_at,relation_names,position)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,COALESCE(max(position),0)+1024 FROM project_tasks WHERE `+projectWhere+" AND column_id=$12 RETURNING id", args...).Scan(&id)
			} else {
				_, err = tx.Exec(ctx, `UPDATE project_tasks SET title=$4,description=$5,owner_member_id=$6,milestone_id=$7,priority=$8,due_at=$9,tag_ids=$10,status=$11,column_id=$12,blocked_reason=$13,completion_note=$14,change_reason=$15,acceptance=$16,participant_ids=$17,start_at=$18,relation_names=relation_names||$19::jsonb,version=version+1,position=CASE WHEN column_id=$12 THEN position ELSE (SELECT COALESCE(max(position),0)+1024 FROM project_tasks WHERE `+projectWhere+" AND column_id=$12) END WHERE "+projectWhere+" AND id=$20", append(args, id)...)
			}
			if err != nil {
				return nil, err
			}
			if err = k.bump(ctx, tx); err != nil {
				return nil, err
			}
			task, err := k.task(ctx, tx, id, time.Now())
			if err != nil {
				return nil, err
			}
			return task, k.history(ctx, tx, a, action, old, task)
		})
		return err
	})
	return result, err
}
