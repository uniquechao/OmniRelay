package project

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// Only these fixed domain operations are callable. Actor and project are supplied by the
// authenticated Gateway transaction. The adapter invokes the same validators and mutations as the UI.
func (s *Service) runTaskBoardTool(ctx context.Context, k taskScope, actor identity.Principal, name string, args pmtools.Args) (any, error) {
	var handle func(http.ResponseWriter, *http.Request, identity.Principal) (any, error)
	var method, resource, pathParam string
	switch name {
	case "task_actions":
		method, handle = "GET", s.taskActions
		resource, pathParam = "task_id", "taskID"
	case "task_milestone_list":
		method, handle = "GET", s.taskMilestones
	case "task_milestone_create":
		method, handle = "POST", s.createMilestone
	case "task_evidence_link":
		method, handle = "PUT", s.linkEvidence
		resource, pathParam = "task_id", "taskID"
	case "task_source_tasks":
		method, handle = "GET", s.sourceTasks
	case "task_business_time":
		method, handle = "GET", s.businessTime
	case "task_board_timezone":
		method, handle = "PUT", s.setBoardTimezone
	case "task_board_get":
		method, handle = "GET", s.readBoard
	case "task_query":
		method, handle = "GET", s.flatTaskQuery
	case "task_move":
		method, handle = "POST", s.moveTask
		resource, pathParam = "task_id", "taskID"
	case "task_bulk_update":
		method, handle = "POST", s.bulkTasks
	case "task_board_save":
		method, handle = "PUT", s.saveBoard
	case "task_tag_create":
		method, handle = "POST", s.boardTagCommand
	case "task_tag_update":
		method, handle = "PUT", s.boardTagCommand
		resource, pathParam = "tag_id", "tagID"
	case "task_tag_delete":
		method, handle = "DELETE", s.boardTagCommand
		resource, pathParam = "tag_id", "tagID"
	case "task_activity_list":
		method, handle = "GET", s.activity
		resource, pathParam = "task_id", "taskID"
	case "task_evidence_list":
		method, handle = "GET", s.evidence
		resource, pathParam = "task_id", "taskID"
	case "task_feedback_list":
		method, handle = "GET", s.suggestions
		resource, pathParam = "task_id", "taskID"
	case "task_feedback_submit":
		method, handle = "POST", s.suggest
		resource, pathParam = "task_id", "taskID"
	case "task_feedback_review":
		method, handle = "POST", s.review
		resource, pathParam = "suggestion_id", "suggestionID"
	case "task_candidate_sources":
		method, handle = "GET", s.candidateSources
	case "task_candidate_chat_sources":
		method, handle = "GET", s.chatCandidateSources
	case "task_candidate_submit":
		method, handle = "POST", s.submitCandidates
	case "task_candidate_list":
		method, handle = "GET", s.candidates
	case "task_candidate_review":
		method, handle = "POST", s.reviewCandidate
		resource, pathParam = "candidate_id", "candidateID"
	default:
		return nil, errInput
	}
	payload := pmtools.Args{}
	q := url.Values{}
	for key, value := range args {
		if key == resource || key == "source_kind" || key == "source_id" {
			continue
		}
		if method != http.MethodGet {
			payload[key] = value
			continue
		}
		var list []string
		if key == "tag_ids" {
			if err := json.Unmarshal(value, &list); err != nil {
				return nil, errInput
			}
			q[key] = list
			continue
		}
		var str string
		if json.Unmarshal(value, &str) != nil {
			str = string(value)
		}
		q.Set(key, str)
	}
	if method != http.MethodGet {
		switch name {
		case "task_move", "task_bulk_update", "task_board_save", "task_tag_create", "task_tag_update", "task_tag_delete", "task_feedback_submit", "task_candidate_submit":
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return nil, err
			}
			key := "request_key"
			if name == "task_candidate_submit" {
				key = "operation_id"
			}
			payload[key], _ = json.Marshal(fmt.Sprintf("%x", nonce[:]))
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// readBoard uses its fixed path suffix to select the board presentation.
	req, err := http.NewRequestWithContext(ctx, method, "/board", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("projectID", k.project)
	if name == "task_evidence_link" || name == "task_source_tasks" {
		kind, id := args.String("source_kind"), args.String("source_id")
		if !validSource(kind, id) {
			return nil, errInput
		}
		req.SetPathValue("kind", kind)
		req.SetPathValue("sourceID", id)
	}
	if resource != "" {
		id := args.String(resource)
		if !identifier.MatchString(id) {
			return nil, errInput
		}
		req.SetPathValue(pathParam, id)
	}
	value, err := handle(nil, req, actor)
	if err != nil {
		return nil, err
	}
	// Private feedback text is obtained only through the authorized, revalidated read tool.
	if name == "task_feedback_review" || name == "task_feedback_submit" {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var receipt struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Version int64  `json:"version"`
		}
		if err = json.Unmarshal(data, &receipt); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	return value, nil
}
