package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) localMembers(ctx context.Context, k taskScope) (any, error) {
	rows, err := s.pool.Query(ctx, `SELECT jsonb_build_object('id',m.member_id,'project_id',m.project_id,'display_name',coalesce(nullif(a.name,''),m.member_id),'role',m.role,'active',true,'roster_status','formal') FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.role!='revoked' AND a.active ORDER BY m.member_id`, k.args()...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	return map[string]any{"project_id": k.project, "panel": "members", "data": map[string]any{"project_id": k.project, "members": items}, "total": len(items)}, err
}

func (s *Service) setMember(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, account := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("accountID")
	var in struct {
		Role   string `json:"role"`
		Member string `json:"member_id"`
	}
	if !identifier.MatchString(account) || !decode(w, r, &in) {
		return nil, errInput
	}
	switch in.Role {
	case "manager", "editor", "member", "reader", "revoked":
	default:
		return nil, errInput
	}
	if in.Member != "" && !identifier.MatchString(in.Member) {
		return nil, errInput
	}
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		// 首位管理员由系统角色管理员指派。已有项目由当前有效项目管理员维护。
		var managers int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.role='manager' AND a.active`, k.args()...).Scan(&managers); err != nil {
			return err
		}
		action, project := "project.delegate", k.project
		if managers == 0 {
			if in.Role != "manager" {
				return errDenied
			}
			action, project = "role.manage", ""
		}
		ok, err := s.roles.CheckInOrganizationMutation(ctx, tx, a, action, project)
		if err != nil {
			return err
		}
		if !ok {
			return errDenied
		}
		var oldRole, oldID string
		var active bool
		err = tx.QueryRow(ctx, `SELECT m.role,m.member_id,a.active FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.account_id=$4`, k.args(account)...).Scan(&oldRole, &oldID, &active)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if active && oldRole == "manager" && in.Role != "manager" && managers <= 1 {
			return errConflict
		}
		if in.Member == "" {
			in.Member = oldID
			if in.Member == "" {
				in.Member = account
			}
		}
		if oldID != "" && in.Member != oldID {
			return errConflict
		}
		if in.Role == "revoked" && oldID == "" {
			return errInput
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_members AS m(tenant_key,app_id,project_id,account_id,member_id,role)
 SELECT $1,$2,$3,id,$5,$6 FROM accounts WHERE tenant_key=$1 AND app_id=$2 AND id=$4 AND (active OR $6='revoked')
 ON CONFLICT(tenant_key,app_id,project_id,account_id) DO UPDATE SET role=EXCLUDED.role,generation=CASE WHEN m.role!=EXCLUDED.role THEN gen_random_uuid()::text ELSE m.generation END`, k.args(account, in.Member, in.Role)...)
		if err != nil {
			return taskError(err)
		}
		if tag.RowsAffected() != 1 {
			return errInput
		}
		if in.Role == "revoked" {
			_, err = tx.Exec(ctx, "DELETE FROM project_source_readers WHERE "+projectWhere+" AND account_id=$4", k.args(account)...)
		}
		return err
	})
	return map[string]string{"member_id": in.Member, "role": in.Role}, err
}
func (s *Service) setTag(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	id := r.PathValue("tagID")
	if !identifier.MatchString(id) || !decode(w, r, &in) || strings.TrimSpace(in.Name) == "" || !textLimit(in.Name, 100) {
		return nil, errInput
	}
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		_, err := tx.Exec(ctx, `INSERT INTO project_task_tags VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_key,app_id,project_id,id) DO UPDATE SET name=EXCLUDED.name`, k.args(id, in.Name)...)
		return err
	})
	return map[string]string{"id": id, "name": in.Name}, err
}

func (s *Service) tags(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k := scope(a, r.PathValue("projectID"))
	rows, err := s.pool.Query(r.Context(), "SELECT jsonb_build_object('id',id,'name',name,'project_id',project_id) FROM project_task_tags WHERE "+projectWhere+" ORDER BY name,id", k.args()...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	return map[string]any{"items": items}, err
}
