package drive

import (
	"context"

	"github.com/uniquechao/OmniRelay/internal/identity"
	"golang.org/x/sync/errgroup"
)

type groupScope struct{ chat, project string }

// visibleInProject uses persisted file sources and checks group access once per scope.
func (s *Service) visibleInProject(ctx context.Context, p identity.Principal, id int64, cloudTokens []string, project string) ([]item, error) {
	initial, err := s.candidates(ctx, p, id, cloudTokens, project)
	if err != nil {
		return nil, err
	}
	scopes := []groupScope{}
	seen := map[groupScope]bool{}
	for _, file := range initial {
		scope := groupScope{file.ChatID, file.ProjectID}
		if !seen[scope] {
			scopes = append(scopes, scope)
			seen[scope] = true
		}
	}
	if len(scopes) == 0 {
		return []item{}, nil
	}
	checks, request := errgroup.WithContext(ctx)
	checks.SetLimit(6)
	allowed := make([]bool, len(scopes))
	failures := make([]error, len(scopes))
	for i, scope := range scopes {
		checks.Go(func() error {
			allowed[i], failures[i] = s.canReadGroup(request, p, scope.project, scope.chat)
			return nil
		})
	}
	if err := checks.Wait(); err != nil {
		return nil, err
	}
	allowedScopes := map[groupScope]bool{}
	for i, scope := range scopes {
		if allowed[i] && failures[i] == nil {
			allowedScopes[scope] = true
		}
	}
	if len(allowedScopes) == 0 {
		for _, err := range failures {
			if err != nil {
				return nil, err
			}
		}
		return []item{}, nil
	}
	current, err := s.candidates(ctx, p, id, cloudTokens, project)
	if err != nil {
		return nil, err
	}
	files := current[:0]
	for _, file := range current {
		if allowedScopes[groupScope{file.ChatID, file.ProjectID}] {
			files = append(files, file)
		}
	}
	return assembleFiles(files), nil
}
