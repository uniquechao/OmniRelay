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
	if project == "" || actor.TenantKey != s.tenant || actor.AppID != s.app || actor.AccountID == "" {
		return nil, ErrRevoked
	}
	files, err := s.visibleInProject(ctx, actor, 0, nil, project)
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
