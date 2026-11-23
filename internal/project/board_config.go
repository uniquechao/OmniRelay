package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type lane struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Stage string `json:"stage"`
}
type boardTag struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Type  string `json:"type"`
	Usage int    `json:"usage_count"`
}
type boardConfig struct {
	Columns []lane
	Default string
}

var defaultLanes = []lane{{"todo", "待办", "#64748b", "todo"}, {"in_progress", "进行中", "#3b82f6", "in_progress"}, {"blocked", "受阻", "#ef4444", "blocked"}, {"done", "已完成", "#22c55e", "done"}}

func (k taskScope) config(ctx context.Context, tx pgx.Tx) (boardConfig, error) {
	var c boardConfig
	err := tx.QueryRow(ctx, "SELECT columns,default_column_id FROM project_task_boards WHERE "+projectWhere, k.args()...).Scan(&c.Columns, &c.Default)
	if errors.Is(err, pgx.ErrNoRows) {
		return boardConfig{defaultLanes, "todo"}, nil
	}
	return c, err
}
func (c boardConfig) resolve(id, stage string) (lane, error) {
	for _, l := range c.Columns {
		if id != "" && l.ID == id || id == "" && (stage == "" && l.ID == c.Default || stage != "" && stage == l.Stage) {
			return l, nil
		}
	}
	return lane{}, errInput
}
func (k taskScope) boardTags(ctx context.Context, tx pgx.Tx) ([]boardTag, error) {
	rows, err := tx.Query(ctx, `SELECT g.id,g.name,g.color,g.type,(SELECT count(*) FROM project_tasks t WHERE (t.tenant_key,t.app_id,t.project_id)=(g.tenant_key,g.app_id,g.project_id) AND t.deleted_at IS NULL AND g.id=ANY(t.tag_ids)) FROM project_task_tags g WHERE `+projectWhere+" ORDER BY type,name,id", k.args()...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[boardTag])
}

type configCommand struct {
	Columns    []lane            `json:"columns"`
	Tags       []boardTag        `json:"tags"`
	Default    string            `json:"default_column_id"`
	Expected   int64             `json:"expected_version"`
	Key        string            `json:"request_key"`
	Migrations map[string]string `json:"migrations"`
	Blocked    string            `json:"blocked_reason"`
	Change     string            `json:"change_reason"`
}

var hexColor = regexp.MustCompile("^#[a-fA-F0-9]{6}$")

func (s *Service) saveBoard(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in configCommand
	if !decodeLimit(w, r, &in, 128<<10) || len(in.Columns) == 0 || len(in.Columns) > 30 || len(in.Tags) > 200 || !validKey(in.Key) || !textLimit(in.Change, 4000) || !textLimit(in.Blocked, 4000) {
		return nil, errInput
	}
	lanes := map[string]lane{}
	names := map[string]bool{}
	for _, l := range in.Columns {
		name := strings.TrimSpace(l.Name)
		if !identifier.MatchString(l.ID) || lanes[l.ID].ID != "" || columnNames[l.Stage] == "" || name == "" || names[name] || !textLimit(name, 100) || !hexColor.MatchString(l.Color) {
			return nil, errInput
		}
		lanes[l.ID] = l
		names[name] = true
	}
	if lanes[in.Default].ID == "" {
		return nil, errInput
	}
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.settings", func(tx pgx.Tx, v int64) error {
		var err error
		result, err = k.command(ctx, tx, a, in.Key, []any{"config", in}, func() (any, error) {
			if v != in.Expected {
				return nil, errBoardVersion
			}
			cfg, err := k.config(ctx, tx)
			if err != nil {
				return nil, err
			}
			removed := 0
			for _, old := range cfg.Columns {
				dest := lanes[old.ID]
				if dest.ID == "" {
					removed++
					dest = lanes[in.Migrations[old.ID]]
					if dest.ID == "" {
						return nil, errInput
					}
				}
				if dest.ID == old.ID && dest.Stage == old.Stage {
					continue
				}
				if err := k.migrateLane(ctx, tx, a, old.ID, dest, in); err != nil {
					return nil, err
				}
			}
			if removed != len(in.Migrations) {
				return nil, errInput
			}
			if in.Tags != nil {
				if err := k.replaceBoardTags(ctx, tx, in.Tags); err != nil {
					return nil, err
				}
			}
			_, err = tx.Exec(ctx, "UPDATE project_task_boards SET columns=$4,default_column_id=$5,version=version+1 WHERE "+projectWhere, k.args(in.Columns, in.Default)...)
			if err != nil {
				return nil, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO audit_events(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,details) VALUES ($1,$2,$3,'project.board.updated','project',$4,'success',$5,$6)`, k.tenant, k.app, a.AccountID, k.project, v+1, map[string]any{"before": cfg, "after": in})
			return map[string]any{"version": v + 1, "default_column_id": in.Default, "columns": in.Columns, "tags": in.Tags}, err
		})
		return err
	})
	return result, err
}
func (k taskScope) migrateLane(ctx context.Context, tx pgx.Tx, a identity.Principal, old string, dest lane, in configCommand) error {
	rows, err := tx.Query(ctx, "SELECT id FROM project_tasks WHERE "+projectWhere+" AND column_id=$4 AND deleted_at IS NULL ORDER BY position,id", k.args(old)...)
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
		stage := dest.Stage
		reason := ""
		if t.Status == "cancelled" {
			stage = t.Status
		}
		if stage == "blocked" {
			reason = t.Blocked
			if reason == "" {
				reason = in.Blocked
			}
		}
		if err := transition(t.Status, stage, reason, in.Change, true); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE project_tasks SET column_id=$5,status=$6,blocked_reason=$7,change_reason=$8,version=version+1, position=CASE WHEN column_id=$5 THEN position ELSE (SELECT coalesce(max(position),0)+1024 FROM project_tasks WHERE `+projectWhere+" AND column_id=$5) END WHERE "+projectWhere+" AND id=$4", k.args(id, dest.ID, stage, reason, in.Change)...)
		if err != nil {
			return err
		}
		next, err := k.task(ctx, tx, id, time.Now())
		if err != nil {
			return err
		}
		if err = k.history(ctx, tx, a, "task.moved", t, next); err != nil {
			return err
		}
	}
	return nil
}
func (k taskScope) replaceBoardTags(ctx context.Context, tx pgx.Tx, tags []boardTag) error {
	ids, names := map[string]bool{}, map[string]bool{}
	for _, t := range tags {
		key := strings.TrimSpace(t.Type) + "\n" + strings.TrimSpace(t.Name)
		if !identifier.MatchString(t.ID) || ids[t.ID] || names[key] || strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Type) == "" || !textLimit(t.Type, 50) || !textLimit(t.Name, 100) || !hexColor.MatchString(t.Color) {
			return errInput
		}
		ids[t.ID] = true
		names[key] = true
	}
	previous, err := k.boardTags(ctx, tx)
	if err != nil {
		return err
	}
	for _, old := range previous {
		if !ids[old.ID] {
			if old.Usage != 0 {
				return errConflict
			}
			if _, err = tx.Exec(ctx, "DELETE FROM project_task_tags WHERE "+projectWhere+" AND id=$4", k.args(old.ID)...); err != nil {
				return err
			}
		}
	}
	for _, t := range tags {
		_, err := tx.Exec(ctx, `INSERT INTO project_task_tags(tenant_key,app_id,project_id,id,name,color,type) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_key,app_id,project_id,id) DO UPDATE SET name=excluded.name,color=excluded.color,type=excluded.type`, k.args(t.ID, t.Name, t.Color, t.Type)...)
		if err != nil {
			return err
		}
	}
	return nil
}
