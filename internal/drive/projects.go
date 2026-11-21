package drive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type projectChange struct {
	Version *int64   `json:"project_version"`
	Add     []string `json:"add_project_ids"`
	Remove  []string `json:"remove_project_ids"`
}

func (c projectChange) valid() bool {
	if c.Version == nil || *c.Version < 0 || len(c.Add)+len(c.Remove) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, ids := range [][]string{c.Add, c.Remove} {
		for _, id := range ids {
			if strings.TrimSpace(id) == "" || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

var (
	errProjectConflict = errors.New("文件归属版本冲突")
	errProjectDenied   = errors.New("缺少项目操作权限")
	errFileHidden      = errors.New("文件来源不可读")
)

func (s *Service) registerProjectChanges(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	mux.HandleFunc("PATCH /api/v1/feishu/files/{fileID}/projects", func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth(w, r, identity.RefreshSessionWithCSRF)
		if !ok {
			return
		}
		if p.TenantKey != s.tenant || p.AppID != s.app || p.AccountID == "" {
			problem(w, 403, "forbidden")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("fileID"), 10, 64)
		if err != nil || id <= 0 {
			problem(w, 404, "file_unavailable")
			return
		}
		var change projectChange
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&change) != nil || decoder.Decode(new(any)) != io.EOF || !change.valid() {
			problem(w, 400, "invalid_project_change")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		files, err := s.visible(ctx, p, id, nil)
		if err != nil {
			problem(w, 503, "source_unavailable")
			return
		}
		if len(files) != 1 {
			problem(w, 404, "file_unavailable")
			return
		}
		version, err := s.changeProjects(ctx, p, files[0], change)
		switch {
		case errors.Is(err, errFileHidden):
			problem(w, 404, "file_unavailable")
		case errors.Is(err, errProjectDenied):
			problem(w, 403, "forbidden")
		case errors.Is(err, errProjectConflict):
			problem(w, 409, "project_version_conflict")
		case err != nil:
			problem(w, 503, "source_unavailable")
		default:
			jsonResponse(w, 200, map[string]int64{"project_version": version})
		}
	})
}

func (s *Service) changeProjects(ctx context.Context, p identity.Principal, file item, c projectChange) (int64, error) {
	var version int64
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		const revisionQuery = `SELECT version FROM feishu_file_project_revisions WHERE tenant_key=$1 AND app_id=$2 AND file_id=$3`
		if err := tx.QueryRow(ctx, revisionQuery+" FOR UPDATE", s.tenant, s.app, file.ID).Scan(&version); err != nil {
			return err
		}
		var readable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+authorizedSources+` AND x.message_id=ANY($7))`, s.tenant, s.app, s.generation, p.AccountID, p.OpenID, file.ID, messageIDs(file)).Scan(&readable); err != nil {
			return err
		}
		if !readable {
			return errFileHidden
		}
		for _, id := range slices.Concat(c.Add, c.Remove) {
			allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, p, "project.assign", id)
			if err != nil {
				return err
			}
			if !allowed {
				return errProjectDenied
			}
		}
		if *c.Version != version {
			return errProjectConflict
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('omnirelay.file_project_actor',$1,true)`, p.AccountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM feishu_file_manual_projects WHERE tenant_key=$1 AND app_id=$2 AND file_id=$3 AND project_id=ANY($4)`, s.tenant, s.app, file.ID, c.Remove); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feishu_file_manual_projects SELECT $1,$2,$3,unnest($4::text[]) ON CONFLICT DO NOTHING`, s.tenant, s.app, file.ID, c.Add); err != nil {
			return err
		}
		return tx.QueryRow(ctx, revisionQuery, s.tenant, s.app, file.ID).Scan(&version)
	})
	return version, err
}
