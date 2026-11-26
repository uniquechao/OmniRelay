package group

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

var errClarifyMember = errors.New("several eligible members share the requested name")

// A selected ID must appear as a complete identifier. Names may be used only
// if the directory identifies exactly one visible object with that name.
func namesID(clause, id string) bool {
	fields := strings.FieldsFunc(strings.ToLower(clause), func(r rune) bool {
		return r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
	for _, f := range fields {
		if f == strings.ToLower(id) {
			return true
		}
	}
	return false
}

func (s *Service) checkWriteTargets(ctx context.Context, tx pgx.Tx, w work, c projectCall) error {
	clauses := routing.AuthorizedClauses(w.Address.Text, c.Name)
	instruction := strings.Join(clauses, "；")
	if instruction == "" {
		return access.ErrForbidden
	}
	if (c.Name == "task_create" || c.Name == "task_milestone_create") && !strings.Contains(instruction, c.Args.String("title")) {
		return access.ErrForbidden
	}
	var assignee string
	if c.Name == "task_create" || c.Name == "task_update" {
		title := c.Args.String("title")
		var scoped []string
		for _, clause := range clauses {
			object, owner := routing.TaskAssignment(clause)
			if title != "" && !strings.Contains(object, title) {
				continue
			}
			if len(scoped) > 0 && owner != assignee {
				return access.ErrForbidden
			}
			assignee = owner
			scoped = append(scoped, object)
		}
		if len(scoped) == 0 {
			return access.ErrForbidden
		}
		instruction = strings.Join(scoped, "；")
		if (assignee != "") != (c.Args.String("owner_member_id") != "") {
			return access.ErrForbidden
		}
	}
	verifyTask := func(id string) error {
		var title, code string
		var unique bool
		err := tx.QueryRow(ctx, `SELECT t.title,project_task_code(p.short_code,t.display_number),NOT EXISTS(SELECT FROM project_tasks x WHERE
		 (x.tenant_key,x.app_id,x.project_id,x.title)=(t.tenant_key,t.app_id,t.project_id,t.title) AND x.id<>t.id AND x.deleted_at IS NULL)
		FROM project_tasks t JOIN project_short_codes p USING(tenant_key,app_id,project_id)
		WHERE (t.tenant_key,t.app_id,t.project_id,t.id)=($1,$2,$3,$4) AND t.deleted_at IS NULL`, s.Tenant, s.App, w.Scope.ProjectID, id).Scan(&title, &code, &unique)
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrForbidden
		}
		if err != nil {
			return err
		}
		if namesID(instruction, id) || namesID(instruction, code) || unique && title != "" && strings.Contains(instruction, title) {
			return nil
		}
		return access.ErrForbidden
	}
	if id := c.Args.String("task_id"); id != "" {
		if err := verifyTask(id); err != nil {
			return err
		}
	}
	if c.Name == "task_bulk_update" {
		var items []struct{ ID string }
		if json.Unmarshal(c.Args["items"], &items) != nil {
			return access.ErrForbidden
		}
		for _, i := range items {
			if err := verifyTask(i.ID); err != nil {
				return err
			}
		}
	}
	// A model may translate a human-readable title or identifier but cannot
	// silently substitute a different resource for a destructive command.
	if id := c.Args.String("chat_id"); id != "" && !namesID(instruction, id) {
		raw, err := s.Projects.RunTool(ctx, tx, w.Actor, w.Scope.ProjectID, "chat_list", nil)
		if err != nil {
			return err
		}
		body, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var groups struct{ Items []struct{ ID, Name string } }
		if err = json.Unmarshal(body, &groups); err != nil {
			return err
		}
		found := 0
		requested := false
		for _, g := range groups.Items {
			if g.Name != "" && strings.Contains(instruction, g.Name) {
				found++
				if g.ID == id {
					requested = true
				}
			}
		}
		if !requested || found != 1 {
			return access.ErrForbidden
		}
	}
	for _, field := range []string{"file_ref", "tag_id", "suggestion_id", "source_id"} {
		if ref := c.Args.String(field); ref != "" && !strings.Contains(instruction, ref) {
			return access.ErrForbidden
		}
	}
	if candidate := c.Args.String("candidate_id"); candidate != "" && !namesID(instruction, candidate) {
		var single bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM agent_project_tool_calls WHERE work_id=$1 AND tool='task_candidate_submit'
			AND receipt->'candidate_ids'=jsonb_build_array($2::text))`, w.Work, candidate).Scan(&single)
		if err != nil {
			return err
		}
		if !single {
			return access.ErrForbidden
		}
	}
	if id := c.Args.String("target_task_id"); id != "" {
		if err := verifyTask(id); err != nil {
			return err
		}
	}
	account, member := c.Args.String("account_id"), c.Args.String("owner_member_id")
	if account != "" || member != "" {
		// Candidate accounts can be added before they have a project membership.
		// The project tool separately checks candidate eligibility and delegation.
		sql := `SELECT a.name, NOT EXISTS(SELECT FROM accounts b WHERE (b.tenant_key,b.app_id,b.name)=(a.tenant_key,a.app_id,a.name) AND b.active AND b.id<>a.id) FROM accounts a WHERE (a.tenant_key,a.app_id,a.id)=($1,$2,$3) AND a.active`
		params := []any{s.Tenant, s.App, account}
		if member != "" {
			sql = `SELECT a.name,NOT EXISTS(SELECT FROM project_members n JOIN accounts b ON (b.tenant_key,b.app_id,b.id)=(n.tenant_key,n.app_id,n.account_id)
			WHERE (n.tenant_key,n.app_id,n.project_id)=(m.tenant_key,m.app_id,m.project_id) AND n.role!='revoked' AND b.name=a.name AND b.active AND n.member_id<>m.member_id)
			FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
			WHERE (m.tenant_key,m.app_id,m.project_id,m.member_id)=($1,$2,$3,$4) AND m.role!='revoked' AND a.active`
			params = []any{s.Tenant, s.App, w.Scope.ProjectID, member}
		}
		var display string
		var unique bool
		err := tx.QueryRow(ctx, sql, params...).Scan(&display, &unique)
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrForbidden
		}
		if err != nil {
			return err
		}
		if member != "" {
			target := strings.TrimSpace(strings.TrimPrefix(assignee, "成员"))
			if target == member {
				return nil
			}
			if display == "" || target != display {
				return access.ErrForbidden
			}
		} else {
			if namesID(instruction, account) {
				return nil
			}
			if display == "" || !strings.Contains(instruction, display) {
				return access.ErrForbidden
			}
		}
		if !unique {
			return errors.Join(access.ErrForbidden, errClarifyMember)
		}
	}
	return nil
}
