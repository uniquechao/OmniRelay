package drive

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

//go:embed preview/convert.py
var previewConverter string

// 单实例一次只运行一个解析进程，为主服务保留内存。
var previewSlot = make(chan struct{}, 1)

const previewFileLimit = 16 << 20
const previewRoot = "/api/v1/feishu/files/{fileID}/versions/{versionID}/previews"
const previewOwner = ` tenant_key=$1 AND app_id=$2 AND generation=$3 AND account_id=$4
 AND open_id=$5 AND file_id=$6 AND version_id=$7 `
const previewFresh = `created_at > now()-interval '5 minutes'`

type previewOptions struct{ Page, Sheet, Entry int }
type previewOutput struct {
	Stage string          `json:"stage"`
	Meta  json.RawMessage `json:"meta,omitempty"`
	MIME  string          `json:"mime,omitempty"`
	Body  []byte          `json:"body,omitempty"`
}
type previewRequest struct {
	project string
	reader  identity.Principal
	file    item
	version int64
	sources []string
	options previewOptions
}

func (s *Service) previewArgs(q previewRequest) []any {
	return []any{s.tenant, s.app, s.generation, q.reader.AccountID, q.reader.OpenID, q.file.ID, q.version}
}

func previewParams(r *http.Request) (previewOptions, bool) {
	var o previewOptions
	o.Page = 1
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return o, false
		}
		if key == "source_project" {
			continue
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 0 {
			return o, false
		}
		switch key {
		case "page":
			if n == 0 {
				return o, false
			}
			o.Page = n
		case "sheet":
			o.Sheet = n
		case "entry":
			if n == 0 {
				return o, false
			}
			o.Entry = n
		default:
			return o, false
		}
	}
	return o, true
}

func (s *Service) registerPreviews(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	handle := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		p, ok := auth(w, r, identity.ValidateSession)
		if !ok {
			return
		}
		if p.TenantKey != s.tenant || p.AppID != s.app || p.AccountID == "" || p.OpenID == "" {
			problem(w, 403, "forbidden")
			return
		}
		fid, e1 := strconv.ParseInt(r.PathValue("fileID"), 10, 64)
		version, e2 := strconv.ParseInt(r.PathValue("versionID"), 10, 64)
		if e1 != nil || e2 != nil || fid <= 0 || version <= 0 {
			problem(w, 400, "invalid_version")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		project := r.URL.Query().Get("source_project")
		files, err := s.visibleInProject(ctx, p, fid, nil, project)
		if err != nil {
			problem(w, 503, "source_unavailable")
			return
		}
		if len(files) != 1 {
			problem(w, 403, "forbidden")
			return
		}
		q := previewRequest{project: project, reader: p, file: files[0], version: version, sources: messageIDs(files[0])}
		slices.Sort(q.sources)
		q.sources = slices.Compact(q.sources)
		if r.Method == http.MethodPost {
			q.options, ok = previewParams(r)
			if !ok {
				problem(w, 400, "invalid_preview")
				return
			}
			s.createPreview(ctx, w, q)
			return
		}
		s.readPreview(ctx, w, r, q)
	}
	mux.HandleFunc("POST "+previewRoot, handle)
	mux.HandleFunc("GET "+previewRoot+"/{taskID}", handle)
	mux.HandleFunc("GET "+previewRoot+"/{taskID}/result", handle)
}

func (s *Service) createPreview(ctx context.Context, w http.ResponseWriter, q previewRequest) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	defer tx.Rollback(ctx)
	// 串行分配有限的缓存位置，多个副本可查询同一任务。
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(72361190)"); err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	args := s.previewArgs(q)
	var size int64
	err = tx.QueryRow(ctx, `SELECT h.byte_size FROM feishu_file_versions h WHERE h.file_id=$6 AND h.id=$8
    AND EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7)`+sourceVersion+`h.id))`,
		s.tenant, s.app, s.generation, q.reader.AccountID, q.reader.OpenID, q.file.ID, q.sources, q.version).Scan(&size)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "version_unavailable")
		return
	}
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	settings, _ := json.Marshal(q.options)
	args = append(args, q.sources, string(settings))
	var id, stage string
	var meta json.RawMessage
	err = tx.QueryRow(ctx, `SELECT id,stage,metadata FROM feishu_file_previews WHERE `+previewOwner+`
    AND sources=$8 AND options=$9 AND `+previewFresh+` AND (stage='ready' OR
    (stage IN ('running','queued') AND created_at>now()-interval '1 minute')) ORDER BY created_at DESC LIMIT 1`, args...).Scan(&id, &stage, &meta)
	if err == nil {
		previewStatus(w, id, q.version, stage, meta)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		problem(w, 503, "source_unavailable")
		return
	}
	stage = "queued"
	if size > previewFileLimit {
		stage = "over_limit"
	}
	start := stage == "queued"
	if start {
		select {
		case previewSlot <- struct{}{}:
		default:
			problem(w, 429, "conversion_busy")
			return
		}
		defer func() {
			if start {
				<-previewSlot
			}
		}()
	}
	// 清理过期结果，最多保留 64 个任务；在压力下优先淘汰已结束的任务。
	_, err = tx.Exec(ctx, `DELETE FROM feishu_file_previews WHERE NOT (`+previewFresh+`)
        OR id IN (SELECT id FROM feishu_file_previews WHERE stage NOT IN ('queued','running')
        OR created_at < now()-interval '1 minute' ORDER BY created_at ASC
        LIMIT greatest(0,(SELECT count(*) FROM feishu_file_previews)-63))`)
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	err = tx.QueryRow(ctx, `INSERT INTO feishu_file_previews(tenant_key,app_id,generation,account_id,open_id,
        file_id,version_id,sources,options,stage) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10
        WHERE (SELECT count(*) FROM feishu_file_previews)<64 RETURNING id`, append(args, stage)...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 429, "conversion_busy")
		return
	}
	if err != nil || tx.Commit(ctx) != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	if start {
		start = false
		go s.runPreview(id, q)
	}
	previewStatus(w, id, q.version, stage, nil)
}

func previewStatus(w http.ResponseWriter, id string, v int64, stage string, meta json.RawMessage) {
	code := http.StatusOK
	if stage == "queued" || stage == "running" {
		code = http.StatusAccepted
	}
	jsonResponse(w, code, map[string]any{"task_id": id, "version_id": v, "stage": stage, "meta": meta})
}

func (s *Service) readPreview(ctx context.Context, w http.ResponseWriter, r *http.Request, q previewRequest) {
	id := r.PathValue("taskID")
	result := strings.HasSuffix(r.Pattern, "/result")
	var stage, mime string
	var meta json.RawMessage
	var body []byte
	args := append(s.previewArgs(q), id, q.sources, result)
	err := s.pool.QueryRow(ctx, `SELECT CASE WHEN stage IN ('running','queued') AND created_at<now()-interval '1 minute'
        THEN 'failed' ELSE stage END,metadata,coalesce(mime,''),CASE WHEN $10 THEN body END
        FROM feishu_file_previews WHERE `+previewOwner+` AND id::text=$8 AND sources && $9 AND `+previewFresh+`
        AND EXISTS (SELECT 1`+authorizedSources+` AND x.message_id=ANY(feishu_file_previews.sources) AND x.message_id=ANY($9)`+sourceVersion+`feishu_file_previews.version_id))`, args...).Scan(&stage, &meta, &mime, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "preview_unavailable")
		return
	}
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	if result && stage == "ready" {
		w.Header().Set("Content-Type", mime)
		w.Write(body)
		return
	}
	previewStatus(w, id, q.version, stage, meta)
}

func (s *Service) runPreview(id string, q previewRequest) {
	defer func() { <-previewSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, "UPDATE feishu_file_previews SET stage='running' WHERE id=$1", id); err != nil {
		return
	}
	out := s.convertVersion(ctx, q)
	if ctx.Err() != nil {
		out = previewOutput{Stage: "over_limit"}
	}
	// 即使转换超时也写回终态；进程重启留下的任务在读取时转为失败。
	finish, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_, _ = s.pool.Exec(finish, `UPDATE feishu_file_previews SET stage=$2,metadata=$3,mime=$4,body=$5 WHERE id=$1`, id, out.Stage, out.Meta, out.MIME, out.Body)
}

func (s *Service) convertVersion(ctx context.Context, q previewRequest) previewOutput {
	files, err := s.visibleInProject(ctx, q.reader, q.file.ID, nil, q.project)
	if err != nil {
		return previewOutput{Stage: "source_unavailable"}
	}
	if len(files) != 1 {
		return previewOutput{Stage: "forbidden"}
	}
	src := messageIDs(files[0])
	src = slices.DeleteFunc(src, func(id string) bool { return !slices.Contains(q.sources, id) })
	info, err := s.versionInfo(ctx, q.reader, q.file, src, q.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return previewOutput{Stage: "forbidden"}
	}
	if err != nil {
		return previewOutput{Stage: "source_unavailable"}
	}
	if info.size > previewFileLimit {
		return previewOutput{Stage: "over_limit"}
	}
	var data bytes.Buffer
	if info.key != "" {
		if s.objects == nil {
			return previewOutput{Stage: "source_unavailable"}
		}
		body, err := s.objects.Open(ctx, info.key, info.size)
		if err != nil {
			return previewOutput{Stage: "source_unavailable"}
		}
		defer body.Close()
		n, err := io.Copy(&data, io.LimitReader(body, info.size+1))
		if err != nil || n != info.size {
			return previewOutput{Stage: "source_unavailable"}
		}
	} else {
		rows, err := s.versionChunks(ctx, q.reader, q.file, src, q.version)
		if err != nil {
			return previewOutput{Stage: "source_unavailable"}
		}
		defer rows.Close()

		found := false
		for rows.Next() {
			var size int64
			var chunk []byte
			if err := rows.Scan(&size, &chunk); err != nil {
				return previewOutput{Stage: "failed"}
			}
			if size > previewFileLimit || data.Len()+len(chunk) > previewFileLimit {
				return previewOutput{Stage: "over_limit"}
			}
			data.Write(chunk)
			found = true
		}
		if rows.Err() != nil {
			return previewOutput{Stage: "source_unavailable"}
		}
		if !found {
			return previewOutput{Stage: "forbidden"}
		}
	}
	kind := q.file.FormatKey
	if q.file.Category == "files" {
		kind = previewFileKind(q.file.Name)
		if kind == "" || kind == "txt" {
			return previewOutput{Stage: "unsupported"}
		}
	}
	return renderPreview(ctx, kind, data.Bytes(), q.options)
}

func renderPreview(ctx context.Context, kind string, data []byte, o previewOptions) previewOutput {
	render, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(render, "python3", "-B", "-c", previewConverter, kind, strconv.Itoa(o.Page), strconv.Itoa(o.Sheet), strconv.Itoa(o.Entry))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONPATH=" + os.Getenv("PYTHONPATH"), "PYTHONNOUSERSITE=1"}
	cmd.Stdin = bytes.NewReader(data)
	b, err := cmd.Output()
	var exit *exec.ExitError
	if render.Err() != nil || (errors.As(err, &exit) && exit.Sys().(syscall.WaitStatus).Signal() == syscall.SIGXCPU) {
		return previewOutput{Stage: "over_limit"}
	}
	var out previewOutput
	if err != nil || json.Unmarshal(b, &out) != nil || out.Stage == "" {
		return previewOutput{Stage: "failed"}
	}
	return out
}
