package drive

import (
	"context"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type ProjectSource struct {
	VersionID *int64
	ID        int64
	Title     string
	Category  string
	FormatKey string
	State     string
	ChatID    string
	MessageID string
	SharedAt  time.Time
}

// 项目入口复用网盘的有效归属、可读群及来源版本筛选。
func (s *Service) ProjectSources(ctx context.Context, actor identity.Principal, project string) ([]ProjectSource, error) {
	files, err := s.ProjectListEntries(ctx, actor, project)
	if err != nil {
		return nil, err
	}
	items := make([]ProjectSource, 0, len(files))
	for _, file := range files {
		source := file.Sources[file.versionSourceIndex]
		category := file.Category
		if category == "images" || category == "videos" {
			category = "files"
		}
		items = append(items, ProjectSource{
			ID: file.ID, VersionID: file.Version, Title: file.Name, Category: category, FormatKey: file.FormatKey, State: file.State,
			ChatID: source.ChatID, MessageID: source.MessageID, SharedAt: source.SentAt,
		})
	}
	return items, nil
}

func (s *Service) ProjectListEntries(ctx context.Context, actor identity.Principal, project string) ([]ListEntry, error) {
	if project == "" || actor.TenantKey != s.tenant || actor.AppID != s.app || actor.AccountID == "" {
		return nil, ErrRevoked
	}
	files, err := s.visibleInProject(ctx, actor, 0, nil, project)
	if err != nil {
		return nil, err
	}
	entries := listEntries(files)
	for i := range entries {
		entry := &entries[i]
		entry.Category = ProjectFileCategory(entry.Category, entry.FormatKey, entry.Name)
	}
	return entries, nil
}

// Check the specific file against current project attribution and source access at write time.
func (s *Service) CanMarkProjectFile(ctx context.Context, actor identity.Principal, project string, id int64) (bool, error) {
	if id <= 0 || project == "" || actor.TenantKey != s.tenant || actor.AppID != s.app || actor.AccountID == "" {
		return false, nil
	}
	files, err := s.visibleInProject(ctx, actor, id, nil, project)
	if err != nil || len(files) != 1 {
		return false, err
	}
	switch files[0].Category {
	case "docs", "files", "minutes":
		return true, nil
	}
	return false, nil
}
