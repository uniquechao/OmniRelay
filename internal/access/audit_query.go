package access

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type auditQuery struct {
	q, kind, result, sort, direction string
	extended                         bool
}

var auditOrder = map[string]string{
	"id": "id", "time": "created_at", "action": "action_label COLLATE audit_text_order",
	"actor": "actor_label COLLATE audit_text_order", "target": "resource_id COLLATE audit_text_order",
	"result": "result COLLATE audit_text_order",
}

func parseAuditQuery(r *http.Request) (auditQuery, error) {
	v, err := url.ParseQuery(r.URL.RawQuery)
	var q auditQuery
	if err != nil {
		return q, errInvalidInput
	}
	for k, values := range v {
		if len(values) != 1 || !utf8.ValidString(values[0]) || strings.ContainsRune(values[0], 0) {
			return q, errInvalidInput
		}
		switch k {
		case "limit", "cursor":
		case "q", "kind", "result", "sort", "direction":
			q.extended = true
		default:
			return q, errInvalidInput
		}
	}
	q.q, q.kind, q.result = strings.TrimSpace(v.Get("q")), v.Get("kind"), v.Get("result")
	q.sort, q.direction = v.Get("sort"), v.Get("direction")
	if !v.Has("sort") {
		q.sort = "id"
	}
	if !v.Has("direction") {
		q.direction = "desc"
	}
	if auditOrder[q.sort] == "" || !slices.Contains([]string{"asc", "desc"}, q.direction) ||
		!slices.Contains([]string{"", "login", "account", "role", "config", "directory"}, q.kind) ||
		!slices.Contains([]string{"", "success", "denied", "cancelled", "failed", "requested", "coalesced", "protected"}, q.result) {
		return q, errInvalidInput
	}
	return q, nil
}

type auditQueryEvent struct {
	AuditEvent
	RecordID    string `json:"recordId"`
	Kind        string `json:"kind"`
	ActionLabel string `json:"actionLabel"`
	ActorLabel  string `json:"actorLabel"`
}
type auditQueryPage struct {
	Items      []auditQueryEvent `json:"items"`
	Total      int64             `json:"total"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func (s *Service) auditCursor(q auditQuery, high, after int64) string {
	boundary := fmt.Sprintf("v1.%d.%d", high, after)
	sum := sha256.Sum256([]byte(strings.Join([]string{boundary, s.tenantKey, s.appID, q.q, q.kind, q.result, q.sort, q.direction}, "\x00")))
	return fmt.Sprintf("%s.%x", boundary, sum[:16])
}

func (s *Service) queryAudit(ctx context.Context, q auditQuery, cursor string, limit int) (auditQueryPage, error) {
	var page auditQueryPage
	var high, after int64
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		if len(parts) != 4 {
			return page, errInvalidInput
		}
		var err error
		high, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return page, errInvalidInput
		}
		after, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil || after < 1 || after > high || cursor != s.auditCursor(q, high, after) {
			return page, errInvalidInput
		}
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		args := pgx.NamedArgs{"tenant": s.tenantKey, "app": s.appID, "limit": limit + 1}
		scope := " FROM audit_query_events WHERE tenant_key=@tenant AND app_id=@app"
		if cursor == "" {
			if err := tx.QueryRow(ctx, "SELECT coalesce(max(id),0)"+scope, args).Scan(&high); err != nil {
				return err
			}
		}
		args["high"] = high
		filter := scope + " AND id<=@high"
		if q.kind != "" {
			filter += " AND kind=@kind"
			args["kind"] = q.kind
		}
		if q.result != "" {
			filter += " AND result=@result"
			args["result"] = q.result
		}
		if q.q != "" {
			filter += " AND strpos(lower(concat_ws(' ',actor_label,action_label,resource_id,id)),lower(@q::text))>0"
			args["q"] = q.q
		}
		if err := tx.QueryRow(ctx, "SELECT count(*)"+filter, args).Scan(&page.Total); err != nil {
			return err
		}
		key := auditOrder[q.sort]
		if cursor != "" {
			op := ">"
			if q.direction == "desc" {
				op = "<"
			}
			filter += " AND (" + key + ",id) " + op + " (SELECT " + key + ",id" + scope + " AND id=@after)"
			args["after"] = after
		}
		order := strings.ToUpper(q.direction)
		rows, err := tx.Query(ctx, `SELECT id,coalesce(actor_account_id,''),action,resource_type,resource_id,result,
			change_version,correlation_id,created_at,directory_sync_run_id,details,kind,action_label,actor_label`+
			filter+" ORDER BY "+key+" "+order+",id "+order+" LIMIT @limit", args)
		if err != nil {
			return err
		}
		page.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (auditQueryEvent, error) {
			var e auditQueryEvent
			err := row.Scan(&e.ID, &e.ActorAccountID, &e.Action, &e.ResourceType, &e.ResourceID, &e.Result,
				&e.ChangeVersion, &e.CorrelationID, &e.CreatedAt, &e.DirectorySyncRunID, &e.Details,
				&e.Kind, &e.ActionLabel, &e.ActorLabel)
			e.CreatedAt = e.CreatedAt.UTC()
			e.RecordID = strconv.FormatInt(e.ID, 10)
			return e, err
		})
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextCursor = s.auditCursor(q, high, page.Items[limit-1].ID)
		}
		return err
	})
	return page, err
}
