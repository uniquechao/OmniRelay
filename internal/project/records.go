package project

import (
	"context"
	"net/url"
	"slices"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type lookupTask func(string) (storedTask, error)

// Read confirmed and merged candidates using the source visibility of the delegated reader.
func (g *Grid) relatedTasks(ctx context.Context, entry mapping, project, kind, id, chat string, lookup lookupTask) ([]storedTask, error) {
	tasks := []storedTask{}
	seen := map[string]bool{}
	for _, status := range []string{"confirmed", "merged"} {
		query := url.Values{"status": {status}, "limit": {"100"}}
		cursors := map[string]bool{}
		for {
			var page struct {
				Items []struct {
					Project  string `json:"project_id"`
					Task     string `json:"task_id"`
					Evidence []struct {
						Type    string `json:"resource_type"`
						Token   string `json:"resource_token"`
						Chat    string `json:"chat_id"`
						Message string `json:"message_id"`
					}
				}
				Next string `json:"next_cursor"`
			}
			if err := g.get(ctx, entry, projectPath(project)+"/task-candidates", query, &page); err != nil {
				return nil, err
			}
			if page.Items == nil {
				return nil, errUnavailable
			}
			for _, item := range page.Items {
				if item.Project != project {
					return nil, errUnavailable
				}
				if seen[item.Task] {
					continue
				}
				for _, ref := range item.Evidence {
					matches := ref.Type == kind && ref.Token == id
					if kind == "message" {
						matches = ref.Chat == chat && ref.Message == id
					}
					if !matches {
						continue
					}
					task, err := lookup(item.Task)
					if err != nil {
						return nil, err
					}
					tasks = append(tasks, task)
					seen[item.Task] = true
					break
				}
			}
			if page.Next == "" {
				break
			}
			if cursors[page.Next] {
				return nil, errUnavailable
			}
			cursors[page.Next] = true
			query.Set("cursor", page.Next)
		}
	}
	return tasks, nil
}

// An empty evidence set alone cannot distinguish an unlinked source from a revoked one.
func (g *Grid) verifySource(ctx context.Context, entry mapping, project, kind, id string) error {
	query := url.Values{"kind": {kind}, "limit": {"100"}}
	seen := map[string]bool{}
	for {
		var page struct {
			Items []struct {
				Ref struct {
					Kind string
					ID   string `json:"source_id"`
				}
				Status string
			}
			Next string `json:"next_cursor"`
		}
		if err := g.get(ctx, entry, projectPath(project)+"/source-content", query, &page); err != nil {
			return err
		}
		if page.Items == nil {
			return errUnavailable
		}
		for _, item := range page.Items {
			if item.Ref.Kind == kind && item.Ref.ID == id {
				if item.Status == "access_denied" {
					return errDenied
				}
				return nil
			}
		}
		if page.Next == "" {
			return errDenied
		}
		if seen[page.Next] {
			return errUnavailable
		}
		seen[page.Next] = true
		query.Set("cursor", page.Next)
	}
}

func (g *Grid) record(ctx context.Context, actor identity.Principal, project, kind, id, chat string, lookup lookupTask) (any, error) {
	if !identifier.MatchString(id) || !slices.Contains([]string{"task", "message", "minutes", "document", "sheets", "bitable", "file"}, kind) || (kind == "message" && !identifier.MatchString(chat)) {
		return nil, errInput
	}
	entry, err := g.credential(ctx, actor)
	if err != nil {
		return nil, err
	}
	before, grant, err := g.resolve(ctx, entry, project)
	if err != nil {
		return nil, err
	}
	if !grant.Member {
		return nil, errDenied
	}
	var value any
	if kind == "task" {
		value, err = lookup(id)
	} else {
		var items []storedTask
		items, err = g.relatedTasks(ctx, entry, project, kind, id, chat, lookup)
		if err == nil && len(items) == 0 && kind != "message" {
			err = g.verifySource(ctx, entry, project, kind, id)
		}
		value = map[string]any{"items": items, "total": len(items)}
	}
	if err != nil {
		return nil, err
	}
	after, current, err := g.resolve(ctx, entry, project)
	if err != nil {
		return nil, err
	}
	if !current.Member {
		return nil, errDenied
	}
	if current.MembershipGeneration != grant.MembershipGeneration || before.Versions["tasks"] == "" || before.Versions["tasks"] != after.Versions["tasks"] {
		return nil, errConflict
	}
	sourceVersion := map[string]string{"minutes": "minutes", "document": "docs", "sheets": "docs", "bitable": "docs", "file": "docs"}[kind]
	if sourceVersion != "" && (before.Versions[sourceVersion] == "" || before.Versions[sourceVersion] != after.Versions[sourceVersion]) {
		return nil, errConflict
	}
	return value, nil
}
