package project

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/drive"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type ProjectFiles interface {
	ProjectText(context.Context, identity.Principal, string, int64, int64, int) (drive.ProjectExcerpt, error)
	ProjectListEntries(context.Context, identity.Principal, string) ([]drive.ListEntry, error)
	ProjectSources(context.Context, identity.Principal, string) ([]drive.ProjectSource, error)
	CanMarkProjectFile(context.Context, identity.Principal, string, int64) (bool, error)
}

func (s *Service) ConfigureFiles(files ProjectFiles) { s.files = files }

type panelView struct {
	Project string         `json:"project_id"`
	Panel   string         `json:"panel"`
	Data    map[string]any `json:"data"`
	Total   *int           `json:"total,omitempty"`
}

func listPanel(id, panel string, items any, total int, data map[string]any) panelView {
	data["items"], data["total"] = items, total
	return panelView{Project: id, Panel: panel, Data: data, Total: &total}
}

func panelPagination(query url.Values) (page, size int, err error) {
	page, size = 1, 25
	if query.Has("page") {
		page, err = strconv.Atoi(query.Get("page"))
		if err != nil || page < 1 || page > 100000 {
			return 0, 0, errInput
		}
	}
	if query.Has("page_size") {
		size, err = strconv.Atoi(query.Get("page_size"))
		if err != nil || size < 1 || size > 100 {
			return 0, 0, errInput
		}
	}
	return page, size, nil
}

func (s *Service) readPanel(ctx context.Context, actor identity.Principal, id, panel string, query url.Values) (any, error) {
	switch panel {
	case "overview":
		return s.overview(ctx, actor, id)
	case "members":
		return s.localMembers(ctx, scope(actor, id), actor, query.Get("purpose") == "assignment")
	case "meetings", "docs":
		return s.sourcePanel(ctx, actor, id, panel, query)
	case "reports":
		return s.reports(ctx, actor, id, query)
	default:
		return nil, errInput
	}
}

type sourceSummary struct {
	Ref       string    `json:"file_ref,omitempty"`
	ID        string    `json:"-"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Created   time.Time `json:"created_at"`
	RefID     string    `json:"resource_token,omitempty"`
	FileID    int64     `json:"file_id,omitempty"`
	VersionID *int64    `json:"version_id,omitempty"`
	Category  string    `json:"category"`
	Status    string    `json:"status,omitempty"`
	ChatID    string    `json:"chat_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
}

func sourceDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if validReportDate(value) {
		return time.Parse(time.DateOnly, value)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return t, errInput
	}
	return t, nil
}

func (s *Service) sourcePanel(ctx context.Context, actor identity.Principal, id, panel string, query url.Values) (panelView, error) {
	page, size, err := panelPagination(query)
	if err != nil {
		return panelView{}, err
	}
	from, err := sourceDate(query.Get("created_from"))
	if err != nil {
		return panelView{}, err
	}
	before, err := sourceDate(query.Get("created_before"))
	if err != nil || !before.IsZero() && !from.IsZero() && from.After(before) {
		return panelView{}, errInput
	}
	coverage := "local"
	items := []sourceSummary{}
	if s.files != nil {
		files, err := s.files.ProjectSources(ctx, actor, id)
		if ctx.Err() != nil {
			return panelView{}, ctx.Err()
		}
		if err != nil {
			coverage = "partial"
			files = nil
		}
		for _, f := range files {
			items = append(items, sourceSummary{
				ID: strconv.FormatInt(f.ID, 10), FileID: f.ID, VersionID: f.VersionID, Kind: f.FormatKey, Title: f.Title, Category: f.Category,
				Status: f.State, ChatID: f.ChatID, MessageID: f.MessageID, Created: f.SharedAt,
			})
		}
	}
	local, err := s.localPanelSources(ctx, actor, id, true)
	if err != nil {
		return panelView{}, err
	}
	items = append(items, local...)
	if panel == "meetings" {
		marks, err := scope(actor, id).meetingFiles(ctx, s.reader())
		if err != nil {
			return panelView{}, err
		}
		for i := range items {
			if marks[fileRef(items[i])] {
				items[i].Category = "minutes"
			}
		}
	}
	for i := range items {
		if panel == "agent_files" {
			items[i].Ref = fileRef(items[i])
		}
	}
	search := strings.ToLower(strings.TrimSpace(query.Get("q")))
	filterKind := query.Get("kind")
	if panel == "meetings" {
		filterKind = query.Get("resource_type")
	}
	items = slices.DeleteFunc(items, func(item sourceSummary) bool {
		return (panel != "agent_files" && (panel == "meetings") != (item.Category == "minutes")) ||
			(query.Has("file_ref") && item.Ref != query.Get("file_ref")) || !strings.Contains(strings.ToLower(item.Title), search) ||
			query.Get("tag") != "" || (query.Get("category") != "" && item.Category != query.Get("category")) ||
			(filterKind != "" && item.Kind != filterKind) ||
			!from.IsZero() && item.Created.Before(from) || !before.IsZero() && !item.Created.Before(before)
	})
	slices.SortFunc(items, func(a, b sourceSummary) int {
		if order := b.Created.Compare(a.Created); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	total := len(items)
	start := min((page-1)*size, total)
	return listPanel(id, panel, items[start:min(start+size, total)], total,
		map[string]any{"coverage": map[string]string{"state": coverage}}), nil
}

func (s *Service) localPanelSources(ctx context.Context, actor identity.Principal, id string, includeMinutes bool) ([]sourceSummary, error) {
	// Read local source grants after the external authorization checks finish.
	rows, err := s.reader().Query(ctx, `SELECT s.id,s.title,s.created_at,s.kind FROM project_sources s
		JOIN project_source_readers r ON (r.tenant_key,r.app_id,r.project_id,r.kind,r.source_id)=(s.tenant_key,s.app_id,s.project_id,s.kind,s.id)
		JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=(r.tenant_key,r.app_id,r.project_id,r.account_id)
		JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
		WHERE (s.tenant_key,s.app_id,s.project_id,r.account_id)=($1,$2,$3,$4) AND (s.kind='document' OR ($6 AND s.kind='minutes'))
		  AND m.role<>'revoked' AND a.active AND a.open_id IS NOT DISTINCT FROM nullif($5,'')`, scope(actor, id).args(actor.AccountID, actor.OpenID, includeMinutes)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (sourceSummary, error) {
		var src sourceSummary
		err := row.Scan(&src.RefID, &src.Title, &src.Created, &src.Kind)
		if src.Kind == "minutes" || drive.ProjectFileCategory("docs", "docs", src.Title) == "minutes" {
			src.Category = "minutes"
		} else {
			src.Category = "docs"
		}
		src.ID = src.RefID
		return src, err
	})
}

func (s *Service) filePage(ctx context.Context, actor identity.Principal, id string, query url.Values) (any, error) {
	var entries []drive.ListEntry
	var partial bool
	if s.files != nil {
		var err error
		entries, err = s.files.ProjectListEntries(ctx, actor, id)
		partial = err != nil
	}
	local, err := s.localPanelSources(ctx, actor, id, false)
	if err != nil {
		return nil, err
	}
	for _, row := range local {
		entries = append(entries, drive.LocalDocument(id, row.RefID, row.Title))
	}
	marks, err := scope(actor, id).meetingFiles(ctx, s.reader())
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entry := &entries[i]
		if entry.LocalSourceID != "" {
			entry.FileRef = "document:" + entry.LocalSourceID
		} else {
			entry.FileRef = "drive:" + strconv.FormatInt(entry.ID, 10)
		}
		entry.MarkedMinutes = marks[entry.FileRef]
	}
	canMark, err := s.allowed(ctx, actor, "project.assign", id)
	if err != nil {
		return nil, err
	}
	page, err := drive.FilePage(query, entries)
	if err != nil {
		return nil, errInput
	}
	page["partial"] = partial
	page["can_mark_minutes"] = canMark
	return page, nil
}
