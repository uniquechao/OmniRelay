package project

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type candidateRef struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type candidateFields struct {
	taskFields
	Confidence float64 `json:"confidence"`
}
type candidateSuggestion struct {
	candidateFields
	Evidence []string `json:"evidence_ids"`
	Related  bool     `json:"related_project"`
}
type chatCandidateScope struct {
	Chat   string    `json:"chat_id"`
	From   time.Time `json:"from_at"`
	To     time.Time `json:"to_at"`
	Limit  int       `json:"limit"`
	Cursor string    `json:"cursor"`
}
type candidateSubmission struct {
	ChatScope  *chatCandidateScope   `json:"chat_scope,omitempty"`
	Key        string                `json:"operation_id"`
	Revision   string                `json:"source_revision"`
	Session    string                `json:"session_ref"`
	Skill      string                `json:"skill_ref"`
	Days       int                   `json:"lookback_days"`
	Candidates []candidateSuggestion `json:"candidates"`
}
type sourceItem struct {
	taskSource
	SourceID string `json:"source_id"`
	Revision string `json:"revision"`
	Part     int    `json:"part"`
}

func fingerprint(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (s *Service) candidateSnapshot(ctx context.Context, tx pgx.Tx, k taskScope, a identity.Principal, days int) ([]sourceItem, error) {
	if days == 0 {
		days = 30
	}
	if days < 1 || days > 365 {
		return nil, errInput
	}
	rows, err := tx.Query(ctx, `SELECT r.kind,r.source_id FROM project_source_readers r JOIN project_sources s ON (s.tenant_key,s.app_id,s.project_id,s.kind,s.id)=(r.tenant_key,r.app_id,r.project_id,r.kind,r.source_id)
 WHERE (r.tenant_key,r.app_id,r.project_id,r.account_id)=($1,$2,$3,$4) AND s.created_at>=current_timestamp-make_interval(days=>$5) ORDER BY s.created_at DESC,r.kind,r.source_id LIMIT 250`, k.args(a.AccountID, days)...)
	if err != nil {
		return nil, err
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidateRef, error) {
		var ref candidateRef
		err := row.Scan(&ref.Kind, &ref.ID)
		return ref, err
	})
	if err != nil {
		return nil, err
	}
	out := []sourceItem{}
	for _, ref := range refs {
		src, err := s.source(ctx, tx, k, a, ref.Kind, ref.ID)
		if errors.Is(err, errDenied) || errors.Is(err, chat.ErrAccessLost) {
			continue
		}
		if err != nil {
			return nil, err
		}
		item := sourceItem{taskSource: src, SourceID: src.ID, Revision: fingerprint(src)}
		item.ID = src.Kind + ":" + src.ID
		out = append(out, item)
	}
	return out, nil
}
func (s *Service) chatCandidatePage(ctx context.Context, tx pgx.Tx, k taskScope, a identity.Principal, scope chatCandidateScope) ([]sourceItem, string, error) {
	if scope.Limit == 0 {
		scope.Limit = 40
	}
	if !identifier.MatchString(scope.Chat) || scope.From.IsZero() || !scope.From.Before(scope.To) || scope.Limit < 1 || scope.Limit > 100 {
		return nil, "", errInput
	}
	if s.chat == nil {
		return nil, "", errUnavailable
	}
	if _, err := s.chat.ProjectGroupForMutation(ctx, tx, a, scope.Chat); err != nil {
		return nil, "", err
	}
	var linked bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM project_chat_assignments WHERE "+projectWhere+" AND chat_id=$4)", k.args(scope.Chat)...).Scan(&linked); err != nil {
		return nil, "", err
	}
	if !linked {
		return nil, "", errDenied
	}
	token := scope.Cursor
	scope.Cursor = ""
	stamp := scope.From
	after := ""
	key := fingerprint([]any{k.tenant, k.app, k.project, a.AccountID, scope})
	type cursor struct {
		Key   string
		Stamp time.Time
		ID    string
	}
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		var c cursor
		if err != nil || json.Unmarshal(b, &c) != nil || c.Key != key || c.Stamp.Before(scope.From) || !c.Stamp.Before(scope.To) || !identifier.MatchString(c.ID) {
			return nil, "", errInput
		}
		stamp, after = c.Stamp, c.ID
	}
	rows, err := tx.Query(ctx, `SELECT m.message_id FROM feishu_chat_messages m JOIN project_source_readers r ON (r.tenant_key,r.app_id,r.kind,r.source_id)=(m.tenant_key,m.app_id,'message',m.message_id)
 WHERE (r.tenant_key,r.app_id,r.project_id,r.account_id)=($1,$2,$3,$4) AND m.chat_id=$5 AND m.visible_in_chat AND NOT m.deleted AND m.sent_at>=$6 AND m.sent_at<$7 AND (m.sent_at,m.message_id)>($8,$9) AND NOT EXISTS(SELECT FROM feishu_candidate_event_owners o WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL) ORDER BY m.sent_at,m.message_id LIMIT $10`, k.args(a.AccountID, scope.Chat, scope.From, scope.To, stamp, after, scope.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, "", err
	}
	more := len(ids) > scope.Limit
	if more {
		ids = ids[:scope.Limit]
	}
	items := []sourceItem{}
	for _, id := range ids {
		src, err := s.source(ctx, tx, k, a, "message", id)
		if err != nil {
			return nil, "", err
		}
		item := sourceItem{taskSource: src, SourceID: id, Revision: fingerprint(src)}
		item.ID = "message:" + id
		items = append(items, item)
	}
	next := ""
	if more {
		last := items[len(items)-1]
		raw, _ := json.Marshal(cursor{key, last.Created, last.SourceID})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return items, next, nil
}
func (s *Service) chatCandidateSources(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	q := r.URL.Query()
	k := scope(a, r.PathValue("projectID"))
	from, err := time.Parse(time.RFC3339, q.Get("from_at"))
	if err != nil {
		return nil, errInput
	}
	to, err := time.Parse(time.RFC3339, q.Get("to_at"))
	if err != nil {
		return nil, errInput
	}
	limit := 40
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 {
			return nil, errInput
		}
	}
	var items []sourceItem
	var next string
	scope := chatCandidateScope{q.Get("chat_id"), from, to, limit, q.Get("cursor")}
	err = s.withReportSnapshot(r.Context(), func(tx pgx.Tx) error {
		var e error
		items, next, e = s.chatCandidatePage(r.Context(), tx, k, a, scope)
		return e
	})
	return map[string]any{"project_id": k.project, "sources": items, "chat_scope": scope, "next_cursor": nilIfEmpty(next), "source_revision": fingerprint(items)}, err
}
func (s *Service) candidateSources(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	days, err := strconv.Atoi(r.URL.Query().Get("lookback_days"))
	if err != nil && r.URL.Query().Has("lookback_days") {
		return nil, errInput
	}
	var items []sourceItem
	err = s.withReportSnapshot(ctx, func(tx pgx.Tx) error { var e error; items, e = s.candidateSnapshot(ctx, tx, k, a, days); return e })
	return map[string]any{"project_id": k.project, "sources": items, "source_revision": fingerprint(items)}, err
}
func (s *Service) submitCandidates(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in candidateSubmission
	if !decodeLimit(w, r, &in, 2<<20) || !validKey(in.Key) || strings.TrimSpace(in.Session) == "" || len(in.Candidates) > 80 {
		return nil, errInput
	}
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	var out json.RawMessage
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		var err error
		out, err = k.command(ctx, tx, a, in.Key, []any{"submit-candidates", in}, func() (any, error) {
			var sources []sourceItem
			var err error
			if in.ChatScope == nil {
				sources, err = s.candidateSnapshot(ctx, tx, k, a, in.Days)
			} else {
				sources, _, err = s.chatCandidatePage(ctx, tx, k, a, *in.ChatScope)
			}
			if err != nil {
				return nil, err
			}
			if fingerprint(sources) != in.Revision {
				return nil, errConflict
			}
			byID := map[string]sourceItem{}
			for _, src := range sources {
				byID[src.ID] = src
			}
			ids := []string{}
			for _, item := range in.Candidates {
				if !item.Related || len(item.Evidence) == 0 || len(item.Evidence) > 40 || item.Confidence < 0 || item.Confidence > 1 || strings.TrimSpace(item.Title) == "" || !textLimit(item.Title, 200) || !textLimit(item.Description, 10000) || !textLimit(item.Acceptance, 4000) {
					return nil, errInput
				}
				if item.Priority != nil && priorityLabels[*item.Priority] == "" {
					return nil, errInput
				}
				for _, d := range []*string{item.Start, item.Due} {
					if d != nil {
						if _, err := time.Parse(time.RFC3339, *d); err != nil {
							return nil, errInput
						}
					}
				}
				if _, err = k.relationNames(ctx, tx, item.taskFields); err != nil {
					return nil, err
				}
				refs := []candidateRef{}
				seen := map[string]bool{}
				for _, id := range item.Evidence {
					src, ok := byID[id]
					if !ok || seen[id] {
						return nil, errInput
					}
					seen[id] = true
					refs = append(refs, candidateRef{src.Kind, src.SourceID, src.Revision})
				}
				var id string
				err = tx.QueryRow(ctx, `INSERT INTO project_task_candidates(tenant_key,app_id,project_id,fields,sources,model_name,prompt_version,submitted_by,fingerprint) VALUES($1,$2,$3,$4,$5,'',$6,$7,$8) ON CONFLICT(tenant_key,app_id,project_id,submitted_by,fingerprint) DO UPDATE SET fingerprint=excluded.fingerprint RETURNING id`, k.args(item.candidateFields, refs, in.Skill, a.AccountID, fingerprint([]any{item.candidateFields, refs}))...).Scan(&id)
				if err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			return map[string]any{"candidate_ids": ids, "operation_id": in.Key}, nil
		})
		return err
	})
	return out, err
}

type candidateRow struct {
	ID       string          `json:"id"`
	Fields   candidateFields `json:"fields"`
	Refs     []candidateRef  `json:"-"`
	Evidence []sourceItem    `json:"evidence"`
	Status   string          `json:"status"`
	Version  int64           `json:"version"`
	Task     *string         `json:"task_id"`
	Skill    string          `json:"prompt_version"`
	Created  time.Time       `json:"created_at"`
}

func (s *Service) candidateEvidence(ctx context.Context, tx pgx.Tx, k taskScope, a identity.Principal, refs []candidateRef) ([]sourceItem, error) {
	items := []sourceItem{}
	for _, ref := range refs {
		src, err := s.source(ctx, tx, k, a, ref.Kind, ref.ID)
		if err != nil {
			return nil, err
		}
		if fingerprint(src) != ref.Revision {
			return nil, errConflict
		}
		items = append(items, sourceItem{taskSource: src, SourceID: src.ID, Revision: ref.Revision})
	}
	return items, nil
}
func (s *Service) candidates(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k, ctx, q := scope(a, r.PathValue("projectID")), r.Context(), r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = "pending_review"
	}
	limit := 25
	offset := 0
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return nil, errInput
		}
	}
	if q.Has("cursor") {
		offset, err = strconv.Atoi(q.Get("cursor"))
		if err != nil || offset < 0 {
			return nil, errInput
		}
	}
	items := []candidateRow{}
	total := 0
	err = s.withReportSnapshot(ctx, func(tx pgx.Tx) error {
		order, ok := map[string]string{"": "created_at DESC,id", "manual": "created_at DESC,id", "priority": "fields->>'priority' NULLS LAST,id", "due": "(fields->>'due_at')::timestamptz NULLS LAST,id", "title": "lower(fields->>'title'),id"}[q.Get("sort")]
		if !ok {
			return errInput
		}
		var overdueBefore any
		if q.Get("overdue_only") == "true" {
			var zone string
			if err := tx.QueryRow(ctx, "SELECT coalesce((SELECT business_timezone FROM project_task_boards WHERE "+projectWhere+"),'Asia/Shanghai')", k.args()...).Scan(&zone); err != nil {
				return err
			}
			loc, err := time.LoadLocation(zone)
			if err != nil {
				return err
			}
			y, m, d := time.Now().In(loc).Date()
			overdueBefore = time.Date(y, m, d, 0, 0, 0, 0, loc)
		}
		for start := 0; ; start += 100 {
			rows, err := tx.Query(ctx, `SELECT id,fields,sources,status,version,task_id,prompt_version,created_at FROM project_task_candidates WHERE `+projectWhere+` AND submitted_by=$4 AND (status=$5 OR $5='superseded' AND status='pending_review') AND ($6='' OR strpos(lower((fields->>'title')||' '||coalesce(fields->>'description','')),lower($6))>0) AND ($7='' OR fields->>'owner_member_id'=$7) AND (NOT $8 OR fields->>'owner_member_id' IS NULL) AND (NOT $9 OR fields->>'due_at' IS NULL) AND ($10::timestamptz IS NULL OR (fields->>'due_at')::timestamptz >= $10) AND ($11::timestamptz IS NULL OR (fields->>'due_at')::timestamptz < $11) AND ($12::text[] IS NULL OR fields->'tag_ids' ?| $12) AND ($13::timestamptz IS NULL OR (fields->>'due_at')::timestamptz < $13) ORDER BY `+order+` LIMIT 100 OFFSET $14`, k.args(a.AccountID, status, q.Get("q"), q.Get("owner_member_id"), q.Get("owner_unassigned") == "true", q.Get("no_due") == "true", nilIfEmpty(q.Get("due_from")), nilIfEmpty(q.Get("due_before")), q["tag_ids"], overdueBefore, start)...)
			if err != nil {
				return err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidateRow, error) {
				var c candidateRow
				err := row.Scan(&c.ID, &c.Fields, &c.Refs, &c.Status, &c.Version, &c.Task, &c.Skill, &c.Created)
				return c, err
			})
			if err != nil {
				return err
			}
			for _, c := range list {
				evidence, err := s.candidateEvidence(ctx, tx, k, a, c.Refs)
				if errors.Is(err, errDenied) || errors.Is(err, chat.ErrAccessLost) {
					continue
				}
				if errors.Is(err, errConflict) {
					if c.Status == "pending_review" {
						c.Status = "superseded"
					}
					c.Fields = candidateFields{taskFields: taskFields{Title: "来源已修订"}}
					evidence = []sourceItem{}
				} else if err != nil {
					return err
				}
				if c.Status != status {
					continue
				}
				if total >= offset && len(items) < limit {
					c.Evidence = evidence
					items = append(items, c)
				}
				total++
			}
			if len(list) < 100 {
				break
			}
		}
		return nil
	})
	var next *string
	if offset+len(items) < total {
		v := strconv.Itoa(offset + len(items))
		next = &v
	}
	return map[string]any{"items": items, "total": total, "next_cursor": next}, err
}
func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

type candidateReview struct {
	Expected      int64       `json:"expected_version"`
	Action        string      `json:"action"`
	Fields        *taskFields `json:"fields"`
	Reason        string      `json:"reason"`
	Target        string      `json:"target_task_id"`
	TargetVersion int64       `json:"target_version"`
	Share         bool        `json:"share_ack"`
	NoDue         bool        `json:"no_deadline_ack"`
}

func (s *Service) reviewCandidate(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in candidateReview
	if !decodeLimit(w, r, &in, 96<<10) || in.Expected < 1 || !textLimit(in.Reason, 2000) {
		return nil, errInput
	}
	k, ctx, id := scope(a, r.PathValue("projectID")), r.Context(), r.PathValue("candidateID")
	var taskID *string
	status := ""
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		var refs []candidateRef
		var version int64
		var replay bool
		err := tx.QueryRow(ctx, `SELECT sources,status,version,task_id,coalesce(review_command=$6::jsonb,false) FROM project_task_candidates WHERE `+projectWhere+" AND id=$4 AND submitted_by=$5", k.args(id, a.AccountID, in)...).Scan(&refs, &status, &version, &taskID, &replay)
		if err != nil {
			return err
		}
		if _, err := s.candidateEvidence(ctx, tx, k, a, refs); err != nil {
			return err
		}
		if replay {
			return nil
		}
		if status != "pending_review" || version != in.Expected {
			return errConflict
		}
		switch in.Action {
		case "ignore":
			if strings.TrimSpace(in.Reason) == "" {
				return errInput
			}
			status = "ignored"
		case "confirm":
			if !in.Share || in.Fields == nil || in.Fields.Due == nil && !in.NoDue {
				return errInput
			}
			bound := *s
			bound.mutation = tx
			out, err := bound.saveTaskInput(ctx, k, "", a, saveTask{taskFields: *in.Fields})
			if err != nil {
				return err
			}
			var t storedTask
			if err = json.Unmarshal(out.(json.RawMessage), &t); err != nil {
				return err
			}
			taskID = &t.ID
			status = "confirmed"
		case "merge":
			if !in.Share {
				return errInput
			}
			t, err := k.task(ctx, tx, in.Target, time.Now())
			if err != nil {
				return err
			}
			if t.Version != in.TargetVersion {
				return errTaskVersion
			}
			taskID = &t.ID
			status = "merged"
			if _, err = tx.Exec(ctx, "UPDATE project_tasks SET version=version+1 WHERE "+projectWhere+" AND id=$4", k.args(t.ID)...); err != nil {
				return err
			}
			if err = k.bump(ctx, tx); err != nil {
				return err
			}
		default:
			return errInput
		}
		if taskID != nil {
			for _, ref := range refs {
				if _, err = tx.Exec(ctx, "INSERT INTO project_task_sources VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", k.args(*taskID, ref.Kind, ref.ID)...); err != nil {
					return err
				}
			}
			if _, err = k.event(ctx, tx, a, *taskID, "change", "task.candidate_linked", in.Reason, map[string]any{}, map[string]any{"建议": id}); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE project_task_candidates SET status=$5,version=version+1,task_id=$6,reviewed_by=$7,review_reason=$8,review_command=$9 WHERE "+projectWhere+" AND id=$4", k.args(id, status, taskID, a.AccountID, in.Reason, in)...)
		return err
	})
	return map[string]any{"status": status, "task_id": taskID}, err
}
