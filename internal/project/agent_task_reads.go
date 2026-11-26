package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// Track the private records actually shown to the model. Reviewing one moves it out
// of the pending page, but final delivery must still check its original source access.
type ToolRead struct {
	Name  string
	Args  pmtools.Args
	Value any
}

const candidateRead = "task_candidate_source_check"
const feedbackRead = "task_feedback_author_check"
const fileReferenceRead = "file_reference_check"

// A reference proves the title and version (or a local excerpt) available
// to the reader. It carries no claim about text from a Drive document.
func fileReferenceValue(result map[string]any) map[string]any {
	item := result["file"].(sourceSummary)
	if item.FileID != 0 {
		return map[string]any{"file": item, "authorized_path": fmt.Sprintf("/api/v1/feishu/files/%d", item.FileID)}
	}
	return map[string]any{"file": item, "excerpt": result["excerpt"]}
}

func ToolReadRecords(name string, args pmtools.Args, value any) ([]ToolRead, error) {
	var out []ToolRead
	add := func(tool, id string, proof any) {
		key, _ := json.Marshal(id)
		out = append(out, ToolRead{tool, pmtools.Args{"id": key}, proof})
	}
	switch name {
	case "file_get":
		result := value.(map[string]any)
		if result["state"] != "ready" {
			return []ToolRead{{fileReferenceRead, args, fileReferenceValue(result)}}, nil
		}
		return []ToolRead{{name, args, value}}, nil
	case "task_candidate_list":
		for _, c := range value.(map[string]any)["items"].([]candidateRow) {
			add(candidateRead, c.ID, []any{c.Fields, c.Evidence})
		}
	case "task_feedback_list":
		for _, raw := range value.(map[string]any)["items"].([]json.RawMessage) {
			var item struct {
				ID    string `json:"id"`
				Actor string `json:"actor_id"`
				Note  string `json:"note"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, err
			}
			add(feedbackRead, item.ID, []string{item.Actor, item.Note})
		}
	default:
		return []ToolRead{{name, args, value}}, nil
	}
	return out, nil
}

func (s *Service) RecheckToolRead(ctx context.Context, tx pgx.Tx, actor identity.Principal, projectID, name string, args pmtools.Args) (any, error) {
	if name == fileReferenceRead {
		result, err := s.RunTool(ctx, tx, actor, projectID, "file_get", args)
		if err != nil {
			return nil, err
		}
		return fileReferenceValue(result.(map[string]any)), nil
	}
	if name != candidateRead && name != feedbackRead {
		return s.RunTool(ctx, tx, actor, projectID, name, args)
	}
	allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.read", projectID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errDenied
	}
	k := scope(actor, projectID)
	if name == feedbackRead {
		grant, err := s.roles.ProjectGrant(ctx, tx, actor, projectID)
		if err != nil {
			return nil, err
		}
		var author, note string
		err = tx.QueryRow(ctx, `SELECT f.actor_id,f.note FROM project_task_suggestions f JOIN project_tasks t ON (f.tenant_key,f.app_id,f.project_id,f.task_id)=(t.tenant_key,t.app_id,t.project_id,t.id)
   WHERE (f.tenant_key,f.app_id,f.project_id,f.id)=($1,$2,$3,$4) AND (f.actor_id=$5 OR $6) AND t.deleted_at IS NULL`, k.args(args.String("id"), actor.AccountID, grant.CanAssign)...).Scan(&author, &note)
		return []string{author, note}, err
	}
	var fields candidateFields
	var refs []candidateRef
	err = tx.QueryRow(ctx, "SELECT fields,sources FROM project_task_candidates WHERE "+projectWhere+" AND id=$4 AND submitted_by=$5", k.args(args.String("id"), actor.AccountID)...).Scan(&fields, &refs)
	if err != nil {
		return nil, err
	}
	evidence, err := s.candidateEvidence(ctx, tx, k, actor, refs)
	if errors.Is(err, errConflict) {
		fields = candidateFields{taskFields: taskFields{Title: "来源已修订"}}
		evidence = []sourceItem{}
		err = nil
	}
	return []any{fields, evidence}, err
}
