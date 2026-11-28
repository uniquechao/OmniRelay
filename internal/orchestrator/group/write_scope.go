package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

var errClarifyMember = errors.New("several eligible members share the requested name")

// The work row is locked by boundWork. Consuming the scope and the domain
// write share a savepoint, so permission errors and conflicts consume neither.
func (s *Service) checkWriteTargets(ctx context.Context, tx pgx.Tx, w work, c projectCall) error {
	if !w.Plan.Allows(c.Name) {
		return access.ErrForbidden
	}
	var conflict error
	for i, a := range w.Plan.Actions {
		if a.Tool != c.Name {
			continue
		}
		err := s.matchFields(ctx, tx, w, a, c.Args)
		if err != nil {
			if !errors.Is(err, access.ErrForbidden) {
				return err
			}
			if errors.Is(err, errClarifyMember) {
				conflict = err
			}
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO agent_action_grants_used(work_id,item,slot,origin_work,origin_item) VALUES($1,$2,$3,nullif($4,''),nullif($5,0)) ON CONFLICT DO NOTHING`, w.Work, i+1, c.Slot, w.OriginWork, w.OriginItem)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
	}
	if conflict != nil {
		return conflict
	}
	return access.ErrForbidden
}
func (s *Service) matchFields(ctx context.Context, tx pgx.Tx, w work, a routing.PlannedAction, args pmtools.Args) error {
	for k := range a.Fields {
		if _, ok := args[k]; !ok {
			return access.ErrForbidden
		}
	}
	for _, k := range a.Generate {
		if _, ok := args[k]; !ok {
			return access.ErrForbidden
		}
	}
	for k, v := range args {
		if routing.VersionField(k) {
			if expected, ok := a.Expected[k]; ok {
				var str string
				if json.Unmarshal(v, &str) != nil {
					str = string(v)
				}
				if str != expected {
					return errSlot
				}
			}
			continue
		}
		if slices.Contains(a.Generate, k) && routing.GeneratedField(a.Tool, k) {
			continue
		}
		wanted, ok := a.Fields[k]
		if target, ok := a.Targets[k]; ok {
			wanted = target
		}
		if !ok {
			return access.ErrForbidden
		}
		var actual string
		if json.Unmarshal(v, &actual) != nil {
			var given, planned any
			if json.Unmarshal(v, &given) != nil || json.Unmarshal([]byte(wanted), &planned) != nil {
				return access.ErrForbidden
			}
			// Bulk updates may refer to a visible task by its unique title or code;
			// optimistic versions in each row are checked by the formal command.
			if k == "participant_ids" || k == "tag_ids" {
				var actuals, refs []string
				if json.Unmarshal(v, &actuals) != nil || json.Unmarshal([]byte(wanted), &refs) != nil || len(actuals) != len(refs) {
					return access.ErrForbidden
				}
				key := "owner_member_id"
				if k == "tag_ids" {
					key = "tag_id"
				}
				for _, ref := range refs {
					found := -1
					for j, id := range actuals {
						e := s.matchSelector(ctx, tx, w, key, id, ref)
						if e == nil {
							found = j
							break
						}
						if !errors.Is(e, access.ErrForbidden) || errors.Is(e, errClarifyMember) {
							return e
						}
					}
					if found < 0 {
						return access.ErrForbidden
					}
					actuals = slices.Delete(actuals, found, found+1)
				}
				continue
			}
			if k == "items" {
				ga, gok := given.([]any)
				pa, pok := planned.([]any)
				if !gok || !pok || len(ga) != len(pa) {
					return access.ErrForbidden
				}
				for i, row := range ga {
					g, gok := row.(map[string]any)
					p, pok := pa[i].(map[string]any)
					if !gok || !pok {
						return access.ErrForbidden
					}
					id, gok := g["id"].(string)
					ref, pok := p["id"].(string)
					if !gok || !pok || !strings.Contains(a.Evidence, ref) {
						return access.ErrForbidden
					}
					if err := s.matchSelector(ctx, tx, w, "task_id", id, ref); err != nil {
						return err
					}
					if v, ok := a.Expected["items."+ref]; ok && fmt.Sprint(g["expected_version"]) != v {
						return errSlot
					}
					delete(g, "expected_version")
					delete(p, "expected_version")
					g["id"] = ref
				}
			}
			if !reflect.DeepEqual(given, planned) {
				return access.ErrForbidden
			}
			continue
		}
		if routing.Selector(k) {
			if err := s.matchSelector(ctx, tx, w, k, actual, wanted); err != nil {
				return err
			}
			continue
		}
		if k == "business_date" {
			resolved, err := s.businessDate(ctx, tx, w, wanted)
			if err != nil {
				return err
			}
			wanted = resolved
		}
		if actual != wanted {
			return access.ErrForbidden
		}
	}
	return nil
}

func (s *Service) businessDate(ctx context.Context, tx pgx.Tx, w work, wanted string) (string, error) {
	if !strings.HasPrefix(wanted, "@") {
		return wanted, nil
	}
	offsets := map[string]int{"@today": 0, "@yesterday": -1, "@tomorrow": 1, "@last_week_end": -1, "@last_week_start": -7}
	offset, ok := offsets[wanted]
	if !ok {
		return "", access.ErrForbidden
	}
	var day string
	err := tx.QueryRow(ctx, `WITH ref AS (SELECT (coalesce(w.asked_at,w.group_asked_at,w.deadline-interval '10 minutes') AT TIME ZONE coalesce(b.business_timezone,'Asia/Shanghai'))::date AS day
 FROM agent_group_work w LEFT JOIN project_task_boards b USING(tenant_key,app_id,project_id) WHERE w.id=$1)
 SELECT ((CASE WHEN $2 THEN date_trunc('week',day)::date ELSE day END)+$3::integer)::text FROM ref`, w.Work, strings.HasPrefix(wanted, "@last_week"), offset).Scan(&day)
	return day, err
}
func (s *Service) matchSelector(ctx context.Context, tx pgx.Tx, w work, k, actual, wanted string) error {
	if actual == wanted {
		return nil
	}
	var display string
	var unique bool
	var err error
	switch k {
	case "task_id", "target_task_id", "before_id", "after_id":
		var code string
		err = tx.QueryRow(ctx, `SELECT t.title,project_task_code(p.short_code,t.display_number),NOT EXISTS(SELECT FROM project_tasks x WHERE
 (x.tenant_key,x.app_id,x.project_id,x.title)=(t.tenant_key,t.app_id,t.project_id,t.title) AND x.id<>t.id AND x.deleted_at IS NULL)
 FROM project_tasks t JOIN project_short_codes p USING(tenant_key,app_id,project_id)
 WHERE (t.tenant_key,t.app_id,t.project_id,t.id)=($1,$2,$3,$4) AND t.deleted_at IS NULL`, s.Tenant, s.App, w.Scope.ProjectID, actual).Scan(&display, &code, &unique)
		if err == nil && code == wanted {
			return nil
		}
	case "owner_member_id", "account_id":
		if k == "account_id" {
			err = tx.QueryRow(ctx, `SELECT a.name,NOT EXISTS(SELECT FROM accounts b WHERE (b.tenant_key,b.app_id,b.name)=(a.tenant_key,a.app_id,a.name) AND b.active AND b.id<>a.id)
 FROM accounts a WHERE (a.tenant_key,a.app_id,a.id)=($1,$2,$3) AND a.active`, s.Tenant, s.App, actual).Scan(&display, &unique)
		} else {
			err = tx.QueryRow(ctx, `SELECT a.name,NOT EXISTS(SELECT FROM project_members n JOIN accounts b ON (b.tenant_key,b.app_id,b.id)=(n.tenant_key,n.app_id,n.account_id)
 WHERE (n.tenant_key,n.app_id,n.project_id)=(m.tenant_key,m.app_id,m.project_id) AND n.role!='revoked' AND b.name=a.name AND b.active AND n.member_id<>m.member_id)
 FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
 WHERE (m.tenant_key,m.app_id,m.project_id,m.member_id)=($1,$2,$3,$4) AND m.role!='revoked' AND a.active`, s.Tenant, s.App, w.Scope.ProjectID, actual).Scan(&display, &unique)
		}
		if err == nil && display == wanted && !unique {
			return errors.Join(access.ErrForbidden, errClarifyMember)
		}
	case "project_id":
		ps, err := project.VisiblePortfolio(ctx, tx, s.Tenant, s.App, w.Actor.AccountID)
		if err != nil {
			return err
		}
		count := 0
		found := false
		for _, p := range ps {
			if p.Name == wanted {
				count++
				found = found || p.ID == actual
			}
		}
		if count == 1 && found {
			return nil
		}
		return access.ErrForbidden
	case "chat_id":
		raw, err := s.Projects.RunTool(ctx, tx, w.Actor, w.Scope.ProjectID, "chat_list", pmtools.Args{})
		if err != nil {
			return err
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var groups struct{ Items []struct{ ID, Name string } }
		if err = json.Unmarshal(b, &groups); err != nil {
			return err
		}
		count := 0
		found := false
		for _, g := range groups.Items {
			if g.Name == wanted {
				count++
				found = found || g.ID == actual
			}
		}
		if count == 1 && found {
			return nil
		}
		return access.ErrForbidden
	case "tag_id", "milestone_id", "column_id", "target_column_id", "default_column_id":
		query := "SELECT name,id FROM project_task_tags WHERE " + scoped + " AND name=$4"
		if k == "milestone_id" {
			query = "SELECT title,id FROM project_milestones WHERE " + scoped + " AND title=$4"
		}
		if strings.HasSuffix(k, "column_id") {
			query = "SELECT c->>'name',c->>'id' FROM project_task_boards, jsonb_array_elements(columns) c WHERE " + scoped + " AND c->>'name'=$4"
		}
		err = tx.QueryRow(ctx, "SELECT count(*)=1 AND coalesce(bool_and(id=$5),false) FROM ("+query+") m(label,id)", s.Tenant, s.App, w.Scope.ProjectID, wanted, actual).Scan(&unique)
		if err != nil {
			return err
		}
		if unique {
			return nil
		}
		return access.ErrForbidden
	case "candidate_id":
		if wanted == "@submitted" {
			err = tx.QueryRow(ctx, `SELECT count(*)=1 AND coalesce(bool_and(receipt->'candidate_ids'=jsonb_build_array($2::text)),false) FROM agent_project_tool_calls WHERE work_id=$1 AND tool='task_candidate_submit'`, w.Work, actual).Scan(&unique)
			if err == nil && unique {
				return nil
			}
			if err != nil {
				return err
			}
		}
		return access.ErrForbidden
	default:
		return access.ErrForbidden
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrForbidden
	}
	if err != nil {
		return err
	}
	if display == wanted && unique {
		return nil
	}
	return access.ErrForbidden
}
