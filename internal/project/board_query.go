package project

import (
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// All caller-provided values are SQL parameters. Counts and pages share this predicate.
func boardPredicate(k taskScope, column string, q url.Values, now time.Time) (string, []any, string, error) {
	args := k.args()
	where := " FROM project_tasks t WHERE " + projectWhere + " AND deleted_at IS NULL"
	bind := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if column != "" {
		where += " AND column_id=" + bind(column)
	}
	for key, col := range map[string]string{"owner_member_id": "owner_member_id", "milestone_id": "milestone_id", "status": "status", "priority": "priority", "filter_column_id": "column_id"} {
		if v := q.Get(key); v != "" {
			where += " AND " + col + "=" + bind(v)
		}
	}
	if q.Get("status") == "" && q.Has("board_view") {
		where += " AND status!='cancelled'"
	}
	for key, col := range map[string]string{"owner_unassigned": "owner_member_id", "milestone_unassigned": "milestone_id", "priority_unset": "priority", "no_due": "due_at"} {
		if q.Get(key) == "true" {
			where += " AND " + col + " IS NULL"
		}
	}
	if ids := q["tag_ids"]; len(ids) > 0 {
		where += " AND tag_ids && " + bind(ids) + "::text[]"
	}
	if term := strings.TrimSpace(q.Get("q")); term != "" {
		arg := bind(term)
		where += " AND (strpos(lower(title||' '||description||' '||coalesce(t.relation_names->>t.owner_member_id,'')||' '||id||' T-'||lpad(display_number::text,greatest(3,length(display_number::text)),'0')),lower(" + arg + "))>0 OR strpos(lower(t.project_id),lower(" + arg + "))>0 OR EXISTS(SELECT FROM project_profiles p WHERE (p.tenant_key,p.app_id,p.project_id)=(t.tenant_key,t.app_id,t.project_id) AND strpos(lower(p.name),lower(" + arg + "))>0) OR EXISTS(SELECT FROM project_members m JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id) WHERE (m.tenant_key,m.app_id,m.project_id,m.member_id)=(t.tenant_key,t.app_id,t.project_id,t.owner_member_id) AND strpos(lower(a.name),lower(" + arg + "))>0))"
	}
	// Older callers stored date-only strings. Interpret these in the project timezone.
	due := "CASE WHEN length(due_at)=10 THEN due_at::date::timestamp AT TIME ZONE " + bind(now.Location().String()) + " ELSE due_at::timestamptz END"
	where += " AND (" + due + " IS NOT NULL OR due_at IS NULL)"
	for key, op := range map[string]string{"due_from": ">=", "due_before": "<"} {
		if v := q.Get(key); v != "" {
			d, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return "", nil, "", errInput
			}
			where += " AND " + due + op + bind(d) + "::timestamptz"
		}
	}
	if q.Get("overdue_only") == "true" {
		y, m, d := now.Date()
		where += " AND status NOT IN ('done','cancelled') AND " + due + "<" + bind(time.Date(y, m, d, 0, 0, 0, 0, now.Location())) + "::timestamptz"
	}
	orders := map[string]string{"": "position,id", "manual": "position,id", "due": due + " NULLS LAST,id", "priority": "priority NULLS LAST,id", "title": "lower(title),id"}
	order, ok := orders[q.Get("sort")]
	if !ok {
		return "", nil, "", errInput
	}
	return where, args, fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)+1, len(args)+2), nil
}

func boardCursorScope(k taskScope, q url.Values, column string) string {
	filters := url.Values{}
	for key, values := range q {
		if key != "cursor" && key != "column_id" {
			filters[key] = values
		}
	}
	return fingerprint([]string{k.tenant, k.app, k.project, column, filters.Encode()})[:24]
}

func (s *Service) flatTaskQuery(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	q := r.URL.Query()
	view := q.Get("view")
	if view != "" && view != "summary" && view != "full" {
		return nil, errInput
	}
	q.Del("view")
	if owner := q.Get("owner"); owner != "" {
		if q.Has("owner_member_id") && q.Get("owner_member_id") != owner {
			return nil, errInput
		}
		q.Set("owner_member_id", owner)
		q.Del("owner")
	}
	q, err := viewQuery("board", q)
	if err != nil {
		return nil, err
	}
	q.Set("board_view", "true")
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	limit := 50
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return nil, errInput
		}
	}
	items := []storedTask{}
	var next *string
	var version int64
	err = s.withReportSnapshot(ctx, func(tx pgx.Tx) error {
		var zone string
		if err := tx.QueryRow(ctx, "SELECT coalesce((SELECT version FROM project_task_boards WHERE "+projectWhere+"),1),coalesce((SELECT business_timezone FROM project_task_boards WHERE "+projectWhere+"),'Asia/Shanghai')", k.args()...).Scan(&version, &zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		now := time.Now().In(loc)
		offset := 0
		signature := boardCursorScope(k, q, "query")
		if token := q.Get("cursor"); token != "" {
			parts := strings.Split(token, ".")
			if len(parts) != 3 || parts[0] != strconv.FormatInt(version, 10) || parts[2] != signature {
				return errConflict
			}
			offset, err = strconv.Atoi(parts[1])
			if err != nil || offset < 1 || offset > 1<<30 {
				return errInput
			}
		}
		filter, args, order, err := boardPredicate(k, "", q, now)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT to_jsonb(t)"+filter+order, append(args, limit+1, offset)...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (storedTask, error) {
			var t storedTask
			if err := row.Scan(&t); err != nil {
				return t, err
			}
			err := t.present(now, version)
			return t, err
		})
		if err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			v := fmt.Sprintf("%d.%d.%s", version, offset+limit, signature)
			next = &v
		}
		return nil
	})
	return map[string]any{"project_id": k.project, "items": items, "next_cursor": next, "version": version}, err
}
