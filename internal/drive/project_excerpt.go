package drive

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/identity"
)

// ProjectExcerpt reads the same immutable page that the project preview shows.
// Only readable text is returned to the model, never embedded images or URLs.
type ProjectExcerpt struct {
	State     string `json:"state"`
	Page      int    `json:"page"`
	Text      string `json:"text,omitempty"`
	Pages     int    `json:"pages,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (s *Service) ProjectText(ctx context.Context, actor identity.Principal, project string, id, version int64, page int) (ProjectExcerpt, error) {
	result := ProjectExcerpt{State: "unavailable", Page: page}
	if project == "" || id <= 0 || version <= 0 || page < 1 || actor.TenantKey != s.tenant || actor.AppID != s.app {
		return result, ErrRevoked
	}
	files, err := s.visibleInProject(ctx, actor, id, nil, project)
	if err != nil {
		return result, err
	}
	if len(files) != 1 || files[0].Version == nil || *files[0].Version != version {
		return result, ErrRevoked
	}
	file := files[0]
	q := previewRequest{reader: actor, project: project, file: file, sources: messageIDs(file), version: version, options: previewOptions{Page: page}}
	switch q.previewKind() {
	case "docs", "minutes", "docx":
	default:
		result.State = "unsupported"
		return result, nil
	}
	select {
	case previewSlot <- struct{}{}:
		defer func() { <-previewSlot }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	out := s.convertVersion(ctx, q)
	result.State = out.Stage
	if out.Stage == "forbidden" {
		return result, ErrRevoked
	}
	if out.Stage != "ready" {
		return result, nil
	}
	var meta struct {
		Pages int `json:"pages"`
	}
	if err := json.Unmarshal(out.Meta, &meta); err != nil {
		return result, err
	}
	result.Pages = meta.Pages
	result.Text, err = excerptText(out.Body, q.previewKind())
	if err != nil {
		return result, err
	}
	runes := []rune(result.Text)
	if len(runes) > 6000 {
		result.Text, result.Truncated = string(runes[:6000]), true
	}
	if result.Text == "" {
		result.State = "no_text"
	}
	return result, nil
}

func excerptText(body []byte, kind string) (string, error) {
	var lines []string
	if kind == "docx" {
		var blocks []struct {
			Text  string
			Table [][]string
		}
		if err := json.Unmarshal(body, &blocks); err != nil {
			return "", err
		}
		for _, block := range blocks {
			lines = append(lines, block.Text)
			for _, row := range block.Table {
				lines = append(lines, strings.Join(row, " · "))
			}
		}
	} else {
		var content struct {
			Text     string
			Document []DocumentBlock
		}
		if err := json.Unmarshal(body, &content); err != nil {
			return "", err
		}
		lines = append(lines, content.Text)
		var collect func([]DocumentBlock)
		collect = func(blocks []DocumentBlock) {
			for _, b := range blocks {
				var line strings.Builder
				for _, r := range b.Runs {
					line.WriteString(r.Text)
				}
				lines = append(lines, line.String())
				collect(b.Children)
				for _, row := range b.Rows {
					for _, cell := range row {
						collect(cell)
					}
				}
			}
		}
		collect(content.Document)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}
