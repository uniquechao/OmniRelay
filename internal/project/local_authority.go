package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) localMembers(ctx context.Context, k taskScope, actor identity.Principal) (any, error) {
	rows, err := s.pool.Query(ctx, `SELECT jsonb_build_object('id',m.member_id,`+userChoiceJSON+`,'project_role',`+access.EffectiveProjectRoleSQL+`,'generation',m.generation,'version',m.version,'role_title',m.role_title,'responsibility',m.responsibility,'project_id',m.project_id,'role',m.role,'active',true,'roster_status','formal') FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)`+userChoiceJoins+`
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.role!='revoked' AND a.active ORDER BY m.member_id`, k.args()...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return nil, err
	}
	canManage, err := s.allowed(ctx, actor, "project.settings", k.project)
	if err != nil {
		return nil, err
	}
	canDelegate, err := s.allowed(ctx, actor, "project.delegate", k.project)
	return map[string]any{"project_id": k.project, "panel": "members", "data": map[string]any{"project_id": k.project, "members": items, "can_manage": canManage, "can_delegate": canDelegate, "project_roles": access.ProjectRoleDefinitions()}, "total": len(items)}, err
}

func (s *Service) setMember(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	ctx, k, account := r.Context(), scope(a, r.PathValue("projectID")), r.PathValue("accountID")
	var in memberChange
	if !decode(w, r, &in) {
		return nil, errInput
	}
	return s.changeMember(ctx, k, a, account, in)
}

type memberChange struct {
	Role                string  `json:"role"`
	Member              string  `json:"member_id"`
	Title               *string `json:"role_title,omitempty"`
	Responsibility      *string `json:"responsibility,omitempty"`
	Expected            *int64  `json:"expected_version,omitempty"`
	ProjectRole         string  `json:"project_role,omitempty"`
	ExpectedProjectRole *string `json:"expected_project_role,omitempty"`
}

func (s *Service) changeMember(ctx context.Context, k taskScope, a identity.Principal, account string, in memberChange) (any, error) {
	if in.ProjectRole != "" && (!access.IsProjectRole(in.ProjectRole) || in.Role != "") {
		return nil, errInput
	}
	if !identifier.MatchString(account) || in.Title != nil && !textLimit(*in.Title, 100) || in.Responsibility != nil && !textLimit(*in.Responsibility, 4000) || in.Expected != nil && *in.Expected < 0 {
		return nil, errInput
	}
	switch in.Role {
	case "", "manager", "editor", "member", "reader", "revoked":
	default:
		return nil, errInput
	}
	if in.Member != "" && !identifier.MatchString(in.Member) {
		return nil, errInput
	}
	err := s.withMutation(ctx, func(tx pgx.Tx) error {
		// 首位管理员由系统角色管理员指派。已有项目由当前有效项目管理员维护。
		var managers int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.role='manager' AND a.active`, k.args()...).Scan(&managers); err != nil {
			return err
		}
		var oldRole, oldID, oldTitle, oldResponsibility string
		var oldVersion int64
		var active bool
		err := tx.QueryRow(ctx, `SELECT m.role,m.member_id,a.active,m.role_title,m.responsibility,m.version FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)`+userChoiceJoins+`
 WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.account_id=$4`, k.args(account)...).Scan(&oldRole, &oldID, &active, &oldTitle, &oldResponsibility, &oldVersion)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		oldProjectRole := access.RoleProjectMember
		if oldRole != "" && oldRole != "revoked" {
			if err := tx.QueryRow(ctx, "SELECT "+access.EffectiveProjectRoleSQL+" FROM project_members m WHERE m.tenant_key=$1 AND m.app_id=$2 AND m.project_id=$3 AND m.account_id=$4", k.args(account)...).Scan(&oldProjectRole); err != nil {
				return err
			}
		}
		if in.ProjectRole != "" {
			if in.ExpectedProjectRole == nil {
				return errInput
			}
			in.Role = oldRole
			if oldRole == "" || oldRole == "revoked" || in.ProjectRole != oldProjectRole {
				in.Role = "member"
			}
			if oldRole == "manager" && in.ProjectRole == access.RoleProjectAdmin {
				in.Role = "manager"
			}
		}
		if in.Role == "" {
			in.Role = oldRole
		}
		action, project := "project.settings", k.project
		projectRoleChange := in.ProjectRole != "" && in.ProjectRole != oldProjectRole
		if in.Role == "manager" || oldRole == "manager" || projectRoleChange || in.Role != oldRole && oldProjectRole == access.RoleProjectAdmin {
			action = "project.delegate"
		}
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
		if in.ExpectedProjectRole != nil && *in.ExpectedProjectRole != oldProjectRole {
			return errConflict
		}
		if in.Expected != nil && *in.Expected != oldVersion {
			return errConflict
		}
		if a.AccountID == account && in.Role != oldRole && in.Role != "revoked" && managers != 0 && oldRole != "manager" {
			return errDenied
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
		if in.Role == "" {
			return errInput
		}
		if in.Role == "revoked" && oldID != "" {
			var unfinished bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_tasks WHERE "+projectWhere+" AND deleted_at IS NULL AND status NOT IN ('done','cancelled') AND (owner_member_id=$4 OR $4=ANY(participant_ids)))", k.args(oldID)...).Scan(&unfinished); err != nil {
				return err
			}
			if unfinished {
				return errConflict
			}
		}
		if projectRoleChange && in.ProjectRole == access.RoleProjectMember {
			if err := s.roles.SetProjectAdministratorInMutation(ctx, tx, a, k.project, account, false); err != nil {
				return err
			}
		}
		title, responsibility := oldTitle, oldResponsibility
		if in.Title != nil {
			title = strings.TrimSpace(*in.Title)
		}
		if in.Responsibility != nil {
			responsibility = strings.TrimSpace(*in.Responsibility)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_members AS m(tenant_key,app_id,project_id,account_id,member_id,role,role_title,responsibility)
 SELECT $1,$2,$3,id,$5,$6,$7,$8 FROM accounts WHERE tenant_key=$1 AND app_id=$2 AND id=$4 AND (active OR $6='revoked')
 ON CONFLICT(tenant_key,app_id,project_id,account_id) DO UPDATE SET role=EXCLUDED.role,role_title=EXCLUDED.role_title,responsibility=EXCLUDED.responsibility,version=m.version+1,generation=CASE WHEN m.role!=EXCLUDED.role THEN gen_random_uuid()::text ELSE m.generation END`, k.args(account, in.Member, in.Role, title, responsibility)...)
		if err != nil {
			return taskError(err)
		}
		if tag.RowsAffected() != 1 {
			return errInput
		}
		if projectRoleChange && in.ProjectRole == access.RoleProjectAdmin {
			if err := s.roles.SetProjectAdministratorInMutation(ctx, tx, a, k.project, account, true); err != nil {
				return err
			}
		}
		if in.Role == "revoked" {
			_, err = tx.Exec(ctx, "DELETE FROM project_source_readers WHERE "+projectWhere+" AND account_id=$4", k.args(account)...)
		}
		if err != nil {
			return err
		}
		detail := map[string]any{"project_id": k.project, "previous_role": oldRole, "role": in.Role, "previous_project_role": oldProjectRole, "project_role": in.ProjectRole, "previous_role_title": oldTitle, "role_title": title, "previous_responsibility": oldResponsibility, "responsibility": responsibility}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,details) VALUES($1,$2,$3,'project.member.update','project_member',$4,'success',$5,$6)`, a.TenantKey, a.AppID, a.AccountID, k.project+":"+account, oldVersion+1, detail)
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
