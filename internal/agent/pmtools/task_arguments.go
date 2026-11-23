package pmtools

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Task tool arrays and objects have the same field names as the task APIs.
type argumentShape struct {
	AnyOf      []*argumentShape          `json:"anyOf,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Properties map[string]*argumentShape `json:"properties,omitempty"`
	Items      *argumentShape            `json:"items,omitempty"`
	Required   []string                  `json:"required,omitempty"`
	Additional any                       `json:"additionalProperties,omitempty"`
	MaxItems   int                       `json:"maxItems,omitempty"`
}

func scalar(kind string) *argumentShape { return &argumentShape{Type: kind} }
func array(item *argumentShape, max int) *argumentShape {
	return &argumentShape{Type: "array", Items: item, MaxItems: max}
}
func object(fields string) *argumentShape {
	s := &argumentShape{Type: "object", Properties: map[string]*argumentShape{}, Additional: false}
	for _, field := range strings.Fields(fields) {
		k := strings.TrimPrefix(field, "!")
		s.Properties[k] = taskField(k)
		if k != field {
			s.Required = append(s.Required, k)
		}
	}
	return s
}
func taskField(k string) *argumentShape {
	switch k {
	case "priority", "owner_member_id", "due_at", "start_at", "milestone_id":
		return &argumentShape{AnyOf: []*argumentShape{scalar("string"), scalar("null")}}
	case "tag_ids", "participant_ids", "evidence_ids", "add_tags", "remove_tags":
		return array(scalar("string"), 50)
	case "confidence":
		return scalar("number")
	case "related_project":
		return scalar("boolean")
	}
	if numeric(k) {
		return scalar("integer")
	}
	return scalar("string")
}
func taskArgument(k string) *argumentShape {
	const editable = "description owner_member_id priority start_at due_at acceptance participant_ids tag_ids"
	switch k {
	case "owner_unassigned", "milestone_unassigned", "priority_unset", "overdue_only", "no_due", "share_ack", "no_deadline_ack":
		return scalar("boolean")
	case "columns":
		return array(object("!id !name !color !stage"), 30)
	case "tags":
		return array(object("!id !name !color !type"), 200)
	case "items":
		return array(object("!id !expected_version title "+editable+" status column_id add_tags remove_tags blocked_reason completion_note change_reason"), 100)
	case "migrations":
		return &argumentShape{Type: "object", Additional: scalar("string")}
	case "candidates":
		return array(object("!title "+editable+" !evidence_ids !related_project confidence"), 80)
	case "chat_scope":
		return object("!chat_id !from_at !to_at limit cursor")
	case "fields":
		return object("!title " + editable + " milestone_id")
	}
	return nil
}
func (s *argumentShape) valid(raw json.RawMessage) bool {
	if len(s.AnyOf) > 0 {
		for _, choice := range s.AnyOf {
			if choice.valid(raw) {
				return true
			}
		}
		return false
	}
	if s.Type == "null" {
		return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	switch s.Type {
	case "object":
		var vals map[string]json.RawMessage
		if json.Unmarshal(raw, &vals) != nil {
			return false
		}
		for _, k := range s.Required {
			if _, ok := vals[k]; !ok {
				return false
			}
		}
		for k, v := range vals {
			prop := s.Properties[k]
			if prop == nil {
				prop, _ = s.Additional.(*argumentShape)
			}
			if prop == nil || !prop.valid(v) {
				return false
			}
		}
	case "array":
		var vals []json.RawMessage
		if json.Unmarshal(raw, &vals) != nil || len(vals) > s.MaxItems {
			return false
		}
		for _, v := range vals {
			if !s.Items.valid(v) {
				return false
			}
		}
	case "boolean":
		var v bool
		return json.Unmarshal(raw, &v) == nil
	case "integer":
		var v int64
		return json.Unmarshal(raw, &v) == nil && v >= 0
	case "number":
		var v float64
		return json.Unmarshal(raw, &v) == nil
	case "string":
		var v string
		return json.Unmarshal(raw, &v) == nil && len(v) <= 16000 && !strings.ContainsRune(v, 0)
	default:
		return false
	}
	return true
}
