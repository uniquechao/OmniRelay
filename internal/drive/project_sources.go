package drive

import (
	"context"
	"time"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

type ProjectSource struct {
	ID        int64
	Title     string
	Category  string
	FormatKey string
	State     string
	ChatID    string
	MessageID string
	SharedAt  time.Time
}

// ProjectSources projects only sources still readable through this project's own group links.
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
		source := file.Sources[0]
		items = append(items, ProjectSource{
			ID: file.ID, Title: file.Name, Category: file.Category, FormatKey: file.FormatKey, State: file.State,
			ChatID: source.ChatID, MessageID: source.MessageID, SharedAt: source.SentAt,
		})
	}
	return items, nil
}
