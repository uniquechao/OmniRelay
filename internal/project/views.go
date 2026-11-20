package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"

	"github.com/uniquechao/OmniRelay/internal/identity"
	"golang.org/x/sync/errgroup"
)

type ProjectPage struct {
	Items    []Project `json:"items"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}

func (g *Grid) directory(ctx context.Context, actor identity.Principal, filter url.Values) (ProjectPage, error) {
	result := ProjectPage{Items: []Project{}}
	entry, err := g.credential(ctx, actor)
	if err != nil {
		return result, err
	}
	items, err := g.candidates(ctx, entry, filter)
	if err != nil {
		return result, err
	}
	seen := make(map[string]bool, len(items))
	visible := make([]bool, len(items))
	group, checks := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, item := range items {
		if seen[item.ID] || !identifier.MatchString(item.ID) {
			// Wait for already scheduled checks before leaving the request scope.
			_ = group.Wait()
			return ProjectPage{}, errUnavailable
		}
		seen[item.ID] = true
		group.Go(func() error {
			_, grant, err := g.resolve(checks, entry, item.ID)
			if errors.Is(err, errDenied) {
				return nil
			}
			visible[i] = grant.Member
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return ProjectPage{}, err
	}
	for i, item := range items {
		if visible[i] {
			result.Items = append(result.Items, item)
		}
	}
	result.Total = len(result.Items)
	return result, nil
}

type gridView struct {
	Project string          `json:"project_id"`
	Panel   string          `json:"panel"`
	Data    json.RawMessage `json:"data"`
	Total   *int            `json:"total,omitempty"`
}

var endpoints = map[string]struct{ path, version, collection string }{
	"overview": {"/workspace", "header", ""},
	"members":  {"/members", "members", "members"},
	"meetings": {"/minutes", "minutes", "page"},
	"docs":     {"/documents", "docs", "page"},
	"reports":  {"/daily-reports/unified", "reports", "page"},
}

func (g *Grid) view(ctx context.Context, actor identity.Principal, id, panel string, query url.Values) (gridView, error) {
	view := gridView{Project: id, Panel: panel}
	endpoint, ok := endpoints[panel]
	if !ok {
		return view, errInput
	}
	entry, err := g.credential(ctx, actor)
	if err != nil {
		return view, err
	}
	before, grant, err := g.resolve(ctx, entry, id)
	if err != nil {
		return view, err
	}
	if !grant.Member {
		return view, errDenied
	}
	if panel == "overview" {
		query.Set("view", "header")
	}
	if err := g.get(ctx, entry, projectPath(id)+endpoint.path, query, &view.Data); err != nil {
		return view, err
	}
	var payload struct {
		ProjectID string `json:"project_id"`
		Project   Project
		Items     []json.RawMessage
		Members   []json.RawMessage
		Columns   []json.RawMessage
		Total     *int
	}
	if json.Unmarshal(view.Data, &payload) != nil || (payload.ProjectID != "" && payload.ProjectID != id) ||
		(payload.Project.ID != "" && payload.Project.ID != id) {
		return view, errUnavailable
	}
	switch endpoint.collection {
	case "members":
		if payload.ProjectID != id || payload.Members == nil {
			return view, errUnavailable
		}
		total := len(payload.Members)
		view.Total = &total
	case "page", "board":
		if payload.Total == nil || *payload.Total < 0 ||
			(endpoint.collection == "page" && (payload.Items == nil || *payload.Total < len(payload.Items))) ||
			(endpoint.collection == "board" && (payload.ProjectID != id || payload.Columns == nil)) {
			return view, errUnavailable
		}
		view.Total = payload.Total
	default:
		if payload.Project.ID != id {
			return view, errUnavailable
		}
		var workspace map[string]json.RawMessage
		if json.Unmarshal(view.Data, &workspace) != nil {
			return view, errUnavailable
		}
		delete(workspace, "has_linked_chats")
		view.Data, err = json.Marshal(workspace)
		if err != nil {
			return view, errUnavailable
		}
		var milestoneData json.RawMessage
		if err := g.get(ctx, entry, projectPath(id)+"/milestones", nil, &milestoneData); err != nil {
			return view, err
		}
		view.Data, err = json.Marshal(map[string]json.RawMessage{"workspace": view.Data, "milestones": milestoneData})
		if err != nil {
			return view, errUnavailable
		}
	}
	after, currentGrant, err := g.resolve(ctx, entry, id)
	if err != nil {
		return view, err
	}
	if !currentGrant.Member {
		return view, errDenied
	}
	if grant.MembershipGeneration != currentGrant.MembershipGeneration ||
		before.Versions[endpoint.version] == "" || before.Versions[endpoint.version] != after.Versions[endpoint.version] ||
		(panel == "overview" && (before.Versions["progress"] == "" || before.Versions["progress"] != after.Versions["progress"])) {
		return view, errConflict
	}
	return view, nil
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
	case "tasks":
		keys = []string{"q", "status", "owner_member_id", "milestone_id", "milestone_unassigned", "limit", "column_id", "cursor"}
	}
	result := make(url.Values)
	for key, values := range input {
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
	if panel == "reports" {
		result.Set("source", "member")
	}
	return result, nil
}

func overviewWithMilestones(raw json.RawMessage, local []map[string]any) (json.RawMessage, error) {
	var data struct {
		Workspace  json.RawMessage            `json:"workspace"`
		Milestones map[string]json.RawMessage `json:"milestones"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var previous []json.RawMessage
	if err := json.Unmarshal(data.Milestones["items"], &previous); err != nil {
		return nil, err
	}
	items := make([]any, 0, len(previous)+len(local))
	for _, raw := range previous {
		var node struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, err
		}
		if node.Kind != "milestone" {
			items = append(items, raw)
		}
	}
	for _, node := range local {
		items = append(items, node)
	}
	var err error
	data.Milestones["items"], err = json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return json.Marshal(data)
}
