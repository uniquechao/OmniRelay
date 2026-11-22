package drive

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type share struct {
	ContextURL string    `json:"context_url"`
	MessageID  string    `json:"message_id"`
	ChatID     string    `json:"chat_id"`
	ChatName   string    `json:"chat_name"`
	SenderID   string    `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	SentAt     time.Time `json:"sent_at"`
	ProjectID  string    `json:"project_id,omitempty"`
	SyncState  string    `json:"sync_state,omitempty"`
}
type candidate struct {
	share
	Projects                       []string
	ProjectVersion                 int64
	ID                             int64
	Kind, Token, Name, State, MIME string
	Version, Size                  *int64
	Updated                        *time.Time
}
type item struct {
	versionSourceIndex int
	ProjectVersion     int64 `json:"project_version"`
	token              string
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Category           string     `json:"category"`
	FormatKey          string     `json:"format_key"`
	Format             string     `json:"format"`
	Size               *int64     `json:"size_bytes"`
	SizeSource         string     `json:"size_source"`
	Sources            []share    `json:"sources"`
	Projects           []string   `json:"project_ids"`
	ManualProjects     []string   `json:"manual_project_ids"`
	InheritedProjects  []string   `json:"inherited_project_ids"`
	State              string     `json:"sync_state"`
	Version            *int64     `json:"version_id"`
	Updated            *time.Time `json:"updated_at"`
}

const authorizedSources = ` FROM feishu_file_sources x
 LEFT JOIN project_chat_links l USING(tenant_key,app_id,chat_id)
 LEFT JOIN project_members pm ON (pm.tenant_key,pm.app_id,pm.project_id,pm.account_id)=(l.tenant_key,l.app_id,l.project_id,$4)
    AND pm.role<>'revoked'
 JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(x.tenant_key,x.app_id,$4)
 LEFT JOIN feishu_file_versions v ON v.id=x.version_id
 WHERE x.tenant_key=$1 AND x.app_id=$2 AND x.credentials_generation=$3
 AND a.open_id=$5 AND a.active AND (l.project_id IS NULL OR pm.account_id IS NOT NULL)
 AND x.chat_kind='group' AND x.state<>'revoked' AND ($6::bigint=0 OR x.file_id=$6)`

func (s *Service) candidates(ctx context.Context, p identity.Principal, id int64, cloudTokens []string, project string) ([]candidate, error) {
	rows, err := s.pool.Query(ctx, `SELECT x.message_id,x.chat_id,x.chat_name,x.sender_id,x.sender_name,x.sent_at,coalesce(l.project_id,''),
 x.file_id,x.kind,x.token,x.name,x.state,coalesce(v.content_type,''),x.version_id,CASE WHEN x.kind IN ('attachment','file','image','video') THEN coalesce(v.byte_size,x.declared_size) END,
 x.synced_at, (SELECT version FROM feishu_file_project_revisions r WHERE
 (r.tenant_key,r.app_id,r.file_id)=(x.tenant_key,x.app_id,x.file_id)),
 ARRAY(SELECT t.project_id FROM feishu_file_manual_projects t JOIN project_members member USING(tenant_key,app_id,project_id)
 WHERE (t.tenant_key,t.app_id,t.file_id)=(x.tenant_key,x.app_id,x.file_id) AND member.account_id=$4
 AND member.role<>'revoked' ORDER BY t.project_id)`+authorizedSources+` AND ($7::text[] IS NULL OR (x.kind='file' AND x.token=ANY($7))) AND ($8='' OR l.project_id=$8 OR EXISTS(SELECT FROM feishu_file_manual_projects mp
 JOIN project_members member USING(tenant_key,app_id,project_id)
 WHERE (mp.tenant_key,mp.app_id,mp.file_id,mp.project_id)=(x.tenant_key,x.app_id,x.file_id,$8)
 AND member.account_id=$4 AND member.role<>'revoked')) ORDER BY x.file_id,x.sent_at DESC,x.message_id`, s.tenant, s.app, s.generation, p.AccountID, p.OpenID, id, cloudTokens, project)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
		var c candidate
		err := r.Scan(&c.MessageID, &c.ChatID, &c.ChatName, &c.SenderID, &c.SenderName, &c.SentAt, &c.ProjectID, &c.ID, &c.Kind, &c.Token, &c.Name, &c.State, &c.MIME, &c.Version, &c.Size, &c.Updated, &c.ProjectVersion, &c.Projects)
		c.ContextURL = "/api/v1/feishu/chats/" + url.PathEscape(c.ChatID) + "/messages/" + url.PathEscape(c.MessageID) + "/context"
		return c, err
	})
}

func (s *Service) visible(ctx context.Context, p identity.Principal, id int64, cloudTokens []string) ([]item, error) {
	return s.visibleInProject(ctx, p, id, cloudTokens, "")
}

func assembleFiles(current []candidate) []item {
	files := []item{}
	positions := map[int64]int{}
	for _, c := range current {
		pos, ok := positions[c.ID]
		if !ok {
			pos = len(files)
			positions[c.ID] = pos
			files = append(files, item{ID: c.ID, ManualProjects: c.Projects, Projects: slices.Clone(c.Projects), InheritedProjects: []string{}, ProjectVersion: c.ProjectVersion, token: c.Token, Name: c.Name, State: c.State, SizeSource: "unknown"})
		}
		f := &files[pos]
		if len(f.Sources) == 0 || (c.Updated != nil && (f.Updated == nil || c.Updated.After(*f.Updated))) {
			f.versionSourceIndex = len(f.Sources)
			f.Version, f.Size, f.Updated, f.Name, f.State = c.Version, c.Size, c.Updated, c.Name, c.State
			f.SizeSource = "unknown"
			if f.Size != nil {
				f.SizeSource = "original"
			}
			format := Format(c.Kind, c.Name)
			if c.Kind == "file" || c.Kind == "attachment" {
				if strings.HasPrefix(c.MIME, "image/") {
					format = formats["image"]
				}
				if strings.HasPrefix(c.MIME, "video/") {
					format = formats["video"]
				}
			}
			f.Category, f.Format, f.FormatKey = format.Category, format.Label, format.Key
		}
		if c.ProjectID != "" {
			if !slices.Contains(f.InheritedProjects, c.ProjectID) {
				f.InheritedProjects = append(f.InheritedProjects, c.ProjectID)
			}
			if !slices.Contains(f.Projects, c.ProjectID) {
				f.Projects = append(f.Projects, c.ProjectID)
			}
		}
		c.share.SyncState = c.State
		f.Sources = append(f.Sources, c.share)
	}
	for i := range files {
		slices.Sort(files[i].Projects)
		slices.Sort(files[i].InheritedProjects)
	}
	return files
}

// CloudFormats 仅为聊天中的云盘链接补充已授权文件的实际格式。
func (s *Service) CloudFormats(ctx context.Context, p identity.Principal, tokens []string) map[string]FileFormat {
	if len(tokens) == 0 || p.TenantKey != s.tenant || p.AppID != s.app {
		return nil
	}
	files, err := s.visible(ctx, p, 0, tokens)
	if err != nil {
		return nil
	}
	result := make(map[string]FileFormat, len(files))
	for _, f := range files {
		result[f.token] = formats[f.FormatKey]
	}
	return result
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
	s.registerPreviews(mux, auth)
	s.registerProjectChanges(mux, auth)
	const base = "/api/v1/feishu/files"
	mux.HandleFunc("GET "+base+"/formats", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth(w, r, identity.ValidateSession); ok {
			jsonResponse(w, 200, formats)
		}
	})
	mux.HandleFunc("GET "+base+"/sync", func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth(w, r, identity.ValidateSession)
		if !ok {
			return
		}
		if p.TenantKey != s.tenant || p.AppID != s.app || p.AccountID == "" {
			problem(w, 403, "forbidden")
			return
		}
		s.getSyncProgress(w, r, p)
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
			files, err := s.visibleInProject(ctx, p, id, nil, r.URL.Query().Get("source_project"))
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

// 每一个可见版本都必须由本次复核通过的来源独立取得过。
const sourceVersion = ` AND EXISTS(SELECT FROM feishu_file_share_versions sv WHERE
 (sv.tenant_key,sv.app_id,sv.message_id,sv.file_id)=(x.tenant_key,x.app_id,x.message_id,x.file_id) AND sv.version_id=`

func (s *Service) versions(ctx context.Context, w http.ResponseWriter, p identity.Principal, f item) {
	rows, err := s.pool.Query(ctx, `SELECT h.id,h.created_at FROM feishu_file_versions h WHERE h.file_id=$6 AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)`+sourceVersion+`h.id)) ORDER BY h.id DESC`, s.tenant, s.app, s.generation, p.AccountID, p.OpenID, f.ID, messageIDs(f))
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
	w.Header().Set("Cache-Control", "private, no-store")
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
	info, err := s.versionInfo(ctx, p, f, messageIDs(f), v)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "file_unavailable")
		return
	}
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	kind := contentType(f, info.mime)
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Has("download") || kind == "application/octet-stream" {
		name := f.Name
		switch f.Category {
		case "docs", "minutes":
			if info.mime == "application/vnd.omnirelay.docx+json" {
				name += ".json"
			} else {
				name += ".txt"
			}
		case "tasks":
			name += ".json"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	if info.key != "" {
		if s.objects == nil {
			problem(w, 503, "source_unavailable")
			return
		}
		object, err := s.objects.Open(ctx, info.key, info.size)
		if err != nil {
			problem(w, 503, "source_unavailable")
			return
		}
		defer object.Close()
		reader := &checkedReader{ReadSeeker: object, check: func() error {
			files, err := s.visibleInProject(ctx, p, f.ID, nil, r.URL.Query().Get("source_project"))
			if err != nil {
				return err
			}
			if len(files) != 1 {
				return ErrRevoked
			}
			_, err = s.versionInfo(ctx, p, f, messageIDs(files[0]), v)
			return err
		}, last: time.Now()}
		http.ServeContent(w, r, f.Name, time.Time{}, reader)
		return
	}
	rows, err := s.versionChunks(ctx, p, f, messageIDs(f), v)
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

var categoryLabels = map[string]string{"docs": "飞书文档", "minutes": "会议纪要", "tasks": "任务表格（多维表格）", "images": "图片", "videos": "视频", "files": "文件"}

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
	if !slices.Contains([]string{"", "name", "size", "category", "project", "format", "shared_at", "updated_at", "source"}, key) || !slices.Contains([]string{"", "asc", "desc"}, order) {
		problem(w, 400, "invalid_sort")
		return
	}
	if key == "" {
		key = "shared_at"
	}
	if key == "shared_at" && order == "" {
		order = "desc"
	}
	projectOptions := map[string]string{}
	selected := []item{}
	term := strings.ToLower(strings.TrimSpace(q.Get("q")))
	for _, f := range files {
		for _, project := range f.Projects {
			projectOptions[project] = project
		}
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
		fields := []string{f.Name, f.Format, f.Category, categoryLabels[f.Category]}
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
		case "shared_at":
			// Each file's authorized sources are already sorted by share time.
			ordering = a.Sources[0].SentAt.Compare(b.Sources[0].SentAt)
		case "updated_at":
			if a.Updated == nil || b.Updated == nil {
				if a.Updated != b.Updated {
					return a.Updated != nil
				}
			} else {
				ordering = a.Updated.Compare(*b.Updated)
			}
		case "name":
			ordering = strings.Compare(a.Name, b.Name)
		case "project":
			ordering = strings.Compare(strings.Join(a.Projects, "、"), strings.Join(b.Projects, "、"))
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
		if order == "desc" {
			return ordering > 0
		}
		return ordering < 0
	})
	total := len(selected)
	offset = min(offset, total)
	end := offset + min(total-offset, limit)
	jsonResponse(w, 200, map[string]any{"projects": projectOptions, "items": selected[offset:end], "total": total, "offset": offset, "limit": limit})
}

func (s *Service) versionChunks(ctx context.Context, p identity.Principal, f item, sources []string, v int64) (pgx.Rows, error) {
	return s.pool.Query(ctx, `SELECT h.byte_size,c.content FROM feishu_file_versions h
 LEFT JOIN LATERAL unnest(h.chunks) WITH ORDINALITY part(digest,ord) ON true
 LEFT JOIN feishu_file_chunks c ON c.file_id=h.file_id AND c.digest=part.digest
 WHERE h.file_id=$6 AND h.id=$8 AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)`+sourceVersion+`h.id)) ORDER BY part.ord`,
		s.tenant, s.app, s.generation, p.AccountID, p.OpenID, f.ID, sources, v)
}

type versionObject struct {
	key, mime string
	size      int64
}

func (s *Service) versionInfo(ctx context.Context, p identity.Principal, f item, messages []string, version int64) (versionObject, error) {
	var obj versionObject
	err := s.pool.QueryRow(ctx, `SELECT coalesce(h.object_key,''),coalesce(h.content_type,''),h.byte_size FROM feishu_file_versions h
    WHERE h.file_id=$6 AND h.id=$8 AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)`+sourceVersion+`h.id))`,
		s.tenant, s.app, s.generation, p.AccountID, p.OpenID, f.ID, messages, version).Scan(&obj.key, &obj.mime, &obj.size)
	return obj, err
}

func contentType(f item, detected string) string {
	if f.Category == "docs" && detected == "application/vnd.omnirelay.docx+json" {
		return "application/json"
	}
	if f.Category == "docs" || f.Category == "minutes" {
		return "text/plain; charset=utf-8"
	}
	if f.Category == "tasks" {
		return "application/json"
	}
	switch detected {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "video/mp4", "video/webm":
		return detected
	}
	return "application/octet-stream"
}

type checkedReader struct {
	io.ReadSeeker
	check func() error
	read  int
	last  time.Time
}

func (r *checkedReader) Read(p []byte) (int, error) {
	if r.read >= 4<<20 || time.Since(r.last) > 5*time.Second {
		if err := r.check(); err != nil {
			return 0, err
		}
		r.read = 0
		r.last = time.Now()
	}
	n, err := r.ReadSeeker.Read(p)
	r.read += n
	return n, err
}
