package drive

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"golang.org/x/sync/errgroup"
)

type share struct {
	MessageID  string    `json:"message_id"`
	ChatID     string    `json:"chat_id"`
	ChatName   string    `json:"chat_name"`
	SenderID   string    `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	SentAt     time.Time `json:"sent_at"`
	ProjectID  string    `json:"project_id"`
}
type candidate struct {
	share
	ID                       int64
	Kind, Token, Name, State string
	Version, Size            *int64
	Updated                  *time.Time
}
type item struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Category   string     `json:"category"`
	FormatKey  string     `json:"format_key"`
	Format     string     `json:"format"`
	Size       *int64     `json:"size_bytes"`
	SizeSource string     `json:"size_source"`
	Sources    []share    `json:"sources"`
	Projects   []string   `json:"project_ids"`
	State      string     `json:"sync_state"`
	Version    *int64     `json:"version_id"`
	Updated    *time.Time `json:"updated_at"`
}

const authorizedSources = ` FROM feishu_file_sources x
 JOIN project_chat_links l USING(tenant_key,app_id,chat_id)
 JOIN feishu_file_projects fp USING(tenant_key,app_id,file_id,project_id)
 JOIN project_members pm USING(tenant_key,app_id,project_id)
 JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(pm.tenant_key,pm.app_id,pm.account_id)
 LEFT JOIN feishu_file_versions v ON v.id=x.version_id
 WHERE x.tenant_key=$1 AND x.app_id=$2 AND x.credentials_generation=$3
 AND pm.account_id=$4 AND a.open_id=$5 AND a.active AND pm.role<>'revoked'
 AND x.chat_kind='group' AND x.state<>'revoked' AND ($6::bigint=0 OR x.file_id=$6)`

func (s *Service) candidates(ctx context.Context, p identity.Principal, id int64) ([]candidate, error) {
	rows, err := s.pool.Query(ctx, `SELECT x.message_id,x.chat_id,x.chat_name,x.sender_id,x.sender_name,x.sent_at,l.project_id,
 x.file_id,x.kind,x.token,x.name,x.state,x.version_id,CASE WHEN x.kind IN ('attachment','file') THEN coalesce(v.byte_size,x.declared_size) END,
 x.synced_at`+authorizedSources+` ORDER BY x.file_id,x.sent_at DESC,x.message_id`, s.tenant, s.app, s.generation, p.AccountID, p.OpenID, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
		var c candidate
		err := r.Scan(&c.MessageID, &c.ChatID, &c.ChatName, &c.SenderID, &c.SenderName, &c.SentAt, &c.ProjectID, &c.ID, &c.Kind, &c.Token, &c.Name, &c.State, &c.Version, &c.Size, &c.Updated)
		return c, err
	})
}

type sourceKey struct {
	file         int64
	msg, project string
}

func (s *Service) visible(ctx context.Context, p identity.Principal, id int64) ([]item, error) {
	initial, err := s.candidates(ctx, p, id)
	if err != nil {
		return nil, err
	}
	var probes sync.Map
	verified := make([]bool, len(initial))
	g, requests := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for i, c := range initial {
		g.Go(func() error {
			ref := Ref{Kind: c.Kind, Token: c.Token, Name: c.Name, MessageID: c.MessageID, ChatKind: "group"}
			err := s.source.CheckShare(requests, ref, c.ChatID)
			if err == nil {
				resource := sourceKey{file: c.ID}
				if c.Kind == "attachment" {
					resource.msg = c.MessageID
				}
				probe, _ := probes.LoadOrStore(resource, sync.OnceValue(func() error { _, probeErr := s.source.ReadFile(requests, ref, true); return probeErr }))
				err = probe.(func() error)()
			}
			if errors.Is(err, ErrRevoked) {
				return nil
			}
			if errors.Is(err, ErrPartial) && c.State == "partial" && id == 0 {
				verified[i] = true
				return nil
			}
			if err != nil {
				return err
			}
			verified[i] = true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	keys := map[sourceKey]bool{}
	for i, c := range initial {
		if verified[i] {
			keys[sourceKey{c.ID, c.MessageID, c.ProjectID}] = true
		}
	}
	current, err := s.candidates(ctx, p, id)
	if err != nil {
		return nil, err
	}
	files := []item{}
	positions := map[int64]int{}
	for _, c := range current {
		if !keys[sourceKey{c.ID, c.MessageID, c.ProjectID}] {
			continue
		}
		pos, ok := positions[c.ID]
		if !ok {
			pos = len(files)
			positions[c.ID] = pos
			files = append(files, item{ID: c.ID, Name: c.Name, State: c.State, SizeSource: "unknown"})
		}
		f := &files[pos]
		if len(f.Sources) == 0 || (c.Updated != nil && (f.Updated == nil || c.Updated.After(*f.Updated))) {
			f.Version, f.Size, f.Updated, f.Name, f.State = c.Version, c.Size, c.Updated, c.Name, c.State
			f.SizeSource = "unknown"
			if f.Size != nil {
				f.SizeSource = "original"
			}
			format := Format(c.Kind, c.Name)
			f.Category, f.Format, f.FormatKey = format.Category, format.Label, format.Key
		}
		f.Sources = append(f.Sources, c.share)
		if !slices.Contains(f.Projects, c.ProjectID) {
			f.Projects = append(f.Projects, c.ProjectID)
		}
	}
	return files, nil
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func problem(w http.ResponseWriter, status int, code string) {
	jsonResponse(w, status, map[string]string{"error": code})
}
func (s *Service) Register(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	const base = "/api/v1/feishu/files"
	mux.HandleFunc("GET "+base+"/formats", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth(w, r, identity.ValidateSession); ok {
			jsonResponse(w, 200, formats)
		}
	})
	for _, pattern := range []string{base, base + "/{fileID}", base + "/{fileID}/versions", base + "/{fileID}/versions/{versionID}/content", base + "/{fileID}/object"} {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth(w, r, identity.ValidateSession)
			if !ok {
				return
			}
			if p.TenantKey != s.tenant || p.AppID != s.app || p.AccountID == "" {
				problem(w, 403, "forbidden")
				return
			}
			var id int64
			if raw := r.PathValue("fileID"); raw != "" {
				var err error
				id, err = strconv.ParseInt(raw, 10, 64)
				if err != nil || id <= 0 {
					problem(w, 404, "file_unavailable")
					return
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			files, err := s.visible(ctx, p, id)
			if err != nil {
				problem(w, 503, "source_unavailable")
				return
			}
			if id == 0 {
				list(w, r, files)
				return
			}
			if len(files) != 1 {
				problem(w, 404, "file_unavailable")
				return
			}
			f := files[0]
			switch pattern {
			case base + "/{fileID}":
				jsonResponse(w, 200, f)
			case base + "/{fileID}/versions":
				s.versions(ctx, w, p, f)
			default:
				s.content(ctx, w, r, p, f)
			}
		})
	}
}
func (s *Service) versions(ctx context.Context, w http.ResponseWriter, p identity.Principal, f item) {
	rows, err := s.pool.Query(ctx, `SELECT h.id,h.created_at FROM feishu_file_versions h WHERE h.file_id=$6 AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)) ORDER BY h.id DESC`, s.tenant, s.app, s.generation, p.AccountID, p.OpenID, f.ID, messageIDs(f))
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	type version struct {
		ID      int64     `json:"id"`
		Created time.Time `json:"created_at"`
	}
	versions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[version])
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	jsonResponse(w, 200, map[string]any{"items": versions})
}
func messageIDs(f item) []string {
	ids := make([]string, 0, len(f.Sources))
	for _, src := range f.Sources {
		ids = append(ids, src.MessageID)
	}
	return ids
}
func (s *Service) content(ctx context.Context, w http.ResponseWriter, r *http.Request, p identity.Principal, f item) {
	var v int64
	if raw := r.PathValue("versionID"); raw != "" {
		v, _ = strconv.ParseInt(raw, 10, 64)
	} else if f.Version != nil {
		v = *f.Version
	}
	if v <= 0 {
		problem(w, 404, "file_unavailable")
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT h.byte_size,c.content FROM feishu_file_versions h
 LEFT JOIN LATERAL unnest(h.chunks) WITH ORDINALITY part(digest,ord) ON true
 LEFT JOIN feishu_file_chunks c ON c.file_id=h.file_id AND c.digest=part.digest
 WHERE h.file_id=$6 AND h.id=$8 AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)) ORDER BY part.ord`,
		s.tenant, s.app, s.generation, p.AccountID, p.OpenID, f.ID, messageIDs(f), v)
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			problem(w, 503, "source_unavailable")
		} else {
			problem(w, 404, "file_unavailable")
		}
		return
	}
	kind := "application/octet-stream"
	if f.Category == "docs" || f.Category == "minutes" {
		kind = "text/plain; charset=utf-8"
	} else if f.Category == "tasks" {
		kind = "application/json"
	}
	w.Header().Set("Content-Type", kind)
	if kind == "application/octet-stream" {
		w.Header().Set("Content-Disposition", "attachment")
	}
	for {
		var b []byte
		var size int64
		if err := rows.Scan(&size, &b); err != nil {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if _, err := w.Write(b); err != nil {
			return
		}
		if !rows.Next() {
			if rows.Err() != nil {
				panic(http.ErrAbortHandler)
			}
			break
		}
	}
}

func list(w http.ResponseWriter, r *http.Request, files []item) {
	q := r.URL.Query()
	offset, limit := 0, 20
	var err error
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 {
			problem(w, 400, "invalid_page")
			return
		}
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			problem(w, 400, "invalid_page")
			return
		}
	}
	key := q.Get("sort")
	order := q.Get("order")
	if !slices.Contains([]string{"", "name", "size", "category", "format", "updated_at", "source"}, key) || !slices.Contains([]string{"", "asc", "desc"}, order) {
		problem(w, 400, "invalid_sort")
		return
	}
	selected := []item{}
	term := strings.ToLower(strings.TrimSpace(q.Get("q")))
	for _, f := range files {
		category := q.Get("category")
		if category != "" && category != "all" && category != f.Category {
			continue
		}
		if format := q.Get("format"); format != "" && f.Format != format && f.FormatKey != format {
			continue
		}
		if project := q.Get("project_id"); project != "" && !slices.Contains(f.Projects, project) {
			continue
		}
		fields := []string{f.Name, f.Format, f.Category}
		fields = append(fields, f.Projects...)
		for _, src := range f.Sources {
			fields = append(fields, src.SenderName, src.ChatName)
		}
		if strings.Contains(strings.ToLower(strings.Join(fields, " ")), term) {
			selected = append(selected, f)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		ordering := 0
		switch key {
		case "size":
			if a.Size == nil || b.Size == nil {
				if a.Size != b.Size {
					return a.Size != nil
				}
			} else {
				ordering = cmp.Compare(*a.Size, *b.Size)
			}
		case "updated_at", "":
			if a.Updated == nil || b.Updated == nil {
				if a.Updated != b.Updated {
					return a.Updated != nil
				}
			} else {
				ordering = a.Updated.Compare(*b.Updated)
			}
		case "name":
			ordering = strings.Compare(a.Name, b.Name)
		case "format":
			ordering = strings.Compare(a.Format, b.Format)
		case "source":
			ordering = strings.Compare(a.Sources[0].ChatName, b.Sources[0].ChatName)
		case "category":
			ordering = strings.Compare(a.Category, b.Category)
		}
		if ordering == 0 {
			return a.ID < b.ID
		}
		if order == "desc" || (order == "" && key == "") {
			return ordering > 0
		}
		return ordering < 0
	})
	total := len(selected)
	offset = min(offset, total)
	end := offset + min(total-offset, limit)
	jsonResponse(w, 200, map[string]any{"items": selected[offset:end], "total": total, "offset": offset, "limit": limit})
}
