package project

import (
	"net/url"
	"slices"
)

type ProjectPage struct {
	Items     []Project           `json:"items"`
	Total     int                 `json:"total"`
	Page      int                 `json:"page"`
	PageSize  int                 `json:"page_size"`
	CanCreate bool                `json:"can_create"`
	Types     []projectTypeOption `json:"project_types"`
}

func viewQuery(panel string, input url.Values) (url.Values, error) {
	var keys []string
	switch panel {
	case "meetings":
		keys = []string{"q", "page", "page_size", "created_from", "created_before", "resource_type"}
	case "docs":
		keys = []string{"q", "page", "page_size", "category", "kind", "tag"}
	case "reports":
		keys = []string{"q", "page", "page_size", "date_from", "date_to", "revision"}
	case "tasks", "board":
		keys = []string{"q", "status", "owner_member_id", "milestone_id", "milestone_unassigned", "limit", "column_id", "cursor"}
	}
	if panel == "board" {
		keys = append(keys, "sort", "priority", "priority_unset", "tag_ids", "tag_match", "no_due", "overdue_only", "due_from", "due_before", "owner_unassigned", "filter_column_id")
	}
	result := make(url.Values)
	for key, values := range input {
		if panel == "board" && key == "tag_ids" {
			if len(values) > 50 {
				return nil, errInput
			}
			for _, v := range values {
				if !identifier.MatchString(v) {
					return nil, errInput
				}
				result.Add(key, v)
			}
			continue
		}
		if !slices.Contains(keys, key) || len(values) != 1 || len(values[0]) > 1024 && (key != "cursor" || len(values[0]) > 1500) {
			return nil, errInput
		}
		result.Set(key, values[0])
	}
	if panel == "tasks" {
		if value, ok := result["milestone_id"]; ok && !identifier.MatchString(value[0]) {
			return nil, errInput
		}
		if value, ok := result["milestone_unassigned"]; ok {
			if value[0] != "true" && value[0] != "false" || value[0] == "true" && result.Has("milestone_id") {
				return nil, errInput
			}
		}
	}
	return result, nil
}
