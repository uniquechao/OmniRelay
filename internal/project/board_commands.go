package project

import (
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"net/http"
	"slices"
	"time"
)

func deltaTags(current, add, remove []string) ([]string, error) {
	tags := slices.Clone(current)
	seen := map[string]bool{}
	for _, list := range [][]string{add, remove} {
		for _, id := range list {
			if !identifier.MatchString(id) || seen[id] {
				return nil, errInput
			}
			seen[id] = true
		}
	}
	for _, id := range add {
		if !slices.Contains(tags, id) {
			tags = append(tags, id)
		}
	}
	tags = slices.DeleteFunc(tags, func(id string) bool { return slices.Contains(remove, id) })
	if len(tags) > 50 {
		return nil, errInput
	}
	return tags, nil
}
func (s *Service) bulkTasks(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		Key   string                       `json:"request_key"`
		Items []map[string]json.RawMessage `json:"items"`
	}
	if !decodeLimit(w, r, &in, 2<<20) || !validKey(in.Key) || len(in.Items) < 1 || len(in.Items) > 100 {
		return nil, errInput
	}
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	var out json.RawMessage
	err := k.write(ctx, s, a, "project.assign", func(tx pgx.Tx, _ int64) error {
		var err error
		out, err = k.command(ctx, tx, a, in.Key, []any{"tasks.bulk_update", in}, func() (any, error) {
			bound := *s
			bound.mutation = tx
			items := []any{}
			seen := map[string]bool{}
			for _, patch := range in.Items {
				var id string
				var expected int64
				if json.Unmarshal(patch["id"], &id) != nil || !identifier.MatchString(id) || seen[id] || json.Unmarshal(patch["expected_version"], &expected) != nil || expected < 1 {
					return nil, errInput
				}
				seen[id] = true
				t, err := k.task(ctx, tx, id, time.Now())
				if err != nil {
					return nil, err
				}
				for key := range patch {
					switch key {
					case "id", "expected_version", "title", "description", "owner_member_id", "participant_ids", "priority", "due_at", "start_at", "acceptance", "tag_ids", "add_tags", "remove_tags", "status", "column_id", "blocked_reason", "completion_note", "change_reason":
					default:
						return nil, errInput
					}
				}
				cmd := saveTask{taskFields: t.taskFields, Expected: expected}
				if patch["add_tags"] != nil || patch["remove_tags"] != nil {
					cmd.Tags = nil
				}
				raw, err := json.Marshal(patch)
				if err != nil {
					return nil, err
				}
				if err = json.Unmarshal(raw, &cmd); err != nil {
					return nil, errInput
				}
				result, err := bound.saveTaskInput(ctx, k, id, a, cmd)
				if err != nil {
					return nil, err
				}
				items = append(items, result)
			}
			return map[string]any{"project_id": k.project, "request_key": in.Key, "items": items}, nil
		})
		return err
	})
	return out, err
}
func (s *Service) boardTagCommand(w http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	var in struct {
		boardTag
		Expected int64  `json:"expected_version"`
		Key      string `json:"request_key"`
	}
	if !decode(w, r, &in) || in.Expected < 1 || !validKey(in.Key) {
		return nil, errInput
	}
	k, ctx := scope(a, r.PathValue("projectID")), r.Context()
	id := r.PathValue("tagID")
	if id == "" {
		id = in.ID
	}
	var result json.RawMessage
	err := k.write(ctx, s, a, "project.settings", func(tx pgx.Tx, v int64) error {
		var err error
		result, err = k.command(ctx, tx, a, in.Key, []any{"board.tag", r.Method, id, in}, func() (any, error) {
			if v != in.Expected {
				return nil, errBoardVersion
			}
			tags, err := k.boardTags(ctx, tx)
			if err != nil {
				return nil, err
			}
			index := slices.IndexFunc(tags, func(t boardTag) bool { return t.ID == id })
			if index < 0 && r.PathValue("tagID") != "" {
				return nil, errInput
			}
			if r.Method == "DELETE" {
				tags = slices.Delete(tags, index, index+1)
			} else {
				if id == "" {
					if err = tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&id); err != nil {
						return nil, err
					}
				}
				in.ID = id
				if index < 0 {
					tags = append(tags, in.boardTag)
				} else {
					tags[index] = in.boardTag
				}
			}
			if err = k.replaceBoardTags(ctx, tx, tags); err != nil {
				return nil, err
			}
			if err = k.bump(ctx, tx); err != nil {
				return nil, err
			}
			tags, err = k.boardTags(ctx, tx)
			return map[string]any{"project_id": k.project, "version": v + 1, "tags": tags}, err
		})
		return err
	})
	return result, err
}

func (s *Service) businessTime(_ http.ResponseWriter, r *http.Request, a identity.Principal) (any, error) {
	k := scope(a, r.PathValue("projectID"))
	var zone string
	err := s.pool.QueryRow(r.Context(), "SELECT coalesce((SELECT business_timezone FROM project_task_boards WHERE "+projectWhere+"),'Asia/Shanghai')", k.args()...).Scan(&zone)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(loc)
	return map[string]any{"project_id": k.project, "timezone": zone, "business_today": now.Format(time.DateOnly), "business_timezone": zone, "business_date": now.Format(time.DateOnly), "now": now}, nil
}
