package project

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type profileInput struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Type         *string `json:"project_type"`
	Background   string  `json:"background"`
	PlannedStart *string `json:"planned_start"`
	PlannedEnd   *string `json:"planned_end"`
	Timezone     *string `json:"business_timezone"`
}

func (in profileInput) apply(p Project) (Project, error) {
	p.Name = strings.TrimSpace(in.Name)
	if in.Type != nil {
		projectType := strings.TrimSpace(*in.Type)
		// Preserve existing custom values when editing unrelated settings.
		if projectType != "" && projectType != p.Type && !slices.ContainsFunc(projectTypeOptions, func(option projectTypeOption) bool {
			return option.Name == projectType
		}) {
			return Project{}, errInput
		}
		p.Type = projectType
	}
	p.Status, p.Background = in.Status, in.Background
	if in.PlannedStart != nil {
		p.PlannedStart = *in.PlannedStart
	}
	if in.PlannedEnd != nil {
		p.PlannedEnd = *in.PlannedEnd
	}
	if in.Timezone != nil {
		p.Timezone = strings.TrimSpace(*in.Timezone)
		if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" || p.Timezone == "Local" {
			return Project{}, errInput
		}
	}
	if p.Name == "" || !textLimit(p.Name, 200) || !textLimit(p.Type, 60) || !textLimit(p.Background, 4000) ||
		strings.ContainsRune(p.Name+p.Type+p.Background, 0) || !slices.Contains(projectStatuses, p.Status) {
		return Project{}, errInput
	}
	for _, date := range []string{p.PlannedStart, p.PlannedEnd} {
		if date == "" {
			continue
		}
		day, err := time.Parse(time.DateOnly, date)
		if err != nil || day.Year() < 1 || day.Format(time.DateOnly) != date {
			return Project{}, errInput
		}
	}
	if p.PlannedEnd != "" && p.PlannedStart > p.PlannedEnd {
		return Project{}, errInput
	}
	return p, nil
}

type profileResult struct {
	ID      string `json:"project_id"`
	Version int64  `json:"version"`
}

var projectCreationKey = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (s *Service) createProject(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in profileInput
	id := r.Header.Get("Idempotency-Key")
	if !decodeLimit(w, r, &in, 32<<10) || id != "" && !projectCreationKey.MatchString(id) {
		return nil, errInput
	}
	if in.Status == "" {
		in.Status = "planned"
	}
	p, err := in.apply(Project{Timezone: "Asia/Shanghai", Version: 1})
	if err != nil || p.Status == "archived" {
		return nil, errInput
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(payload))
	ctx := r.Context()
	err = s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.create", "")
		if err != nil {
			return err
		}
		if !allowed {
			return errDenied
		}
		if id == "" {
			if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
				return err
			}
		}
		k := scope(actor, id)
		var exists, replay bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM project_profiles WHERE `+projectWhere+`
			UNION ALL SELECT 1 FROM project_members WHERE `+projectWhere+`
			UNION ALL SELECT 1 FROM project_task_boards WHERE `+projectWhere+`
			UNION ALL SELECT 1 FROM project_relay_settings WHERE `+projectWhere+`
		), EXISTS (SELECT 1 FROM audit_events
			WHERE (tenant_key,app_id,resource_id,actor_account_id)=($1,$2,$3,$4)
			AND resource_type='project' AND action='project.create' AND details->>'request_fingerprint'=$5)`,
			k.args(actor.AccountID, fingerprint)...).Scan(&exists, &replay); err != nil {
			return err
		}
		if exists {
			if replay {
				allowed, err := s.roles.CheckInOrganizationMutation(ctx, tx, actor, "project.read", id)
				if err != nil || allowed {
					return err
				}
			}
			return errConflict
		}
		if err := k.saveProfile(ctx, tx, p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members (tenant_key,app_id,project_id,account_id,member_id,role)
			VALUES($1,$2,$3,$4,$4,'manager')`, k.args(actor.AccountID)...); err != nil {
			return err
		}
		if err := k.setProfileTimezone(ctx, tx, p.Timezone, 0); err != nil {
			return err
		}
		return k.auditProfile(ctx, tx, actor, "project.create", p.Version, map[string]string{
			"status": p.Status, "project_type": p.Type, "request_fingerprint": fingerprint,
		})
	})
	return profileResult{ID: id, Version: p.Version}, err
}

func (s *Service) setProfile(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		profileInput
		Version      *int64 `json:"expected_version"`
		BoardVersion *int64 `json:"expected_board_version"`
	}
	if !decodeLimit(w, r, &in, 32<<10) || in.Version == nil || *in.Version < 0 ||
		(in.Timezone != nil && (in.BoardVersion == nil || *in.BoardVersion < 0)) {
		return nil, errInput
	}
	return s.changeProject(r, actor, *in.Version, &in.profileInput, in.BoardVersion)
}

func (s *Service) archiveProject(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	var in struct {
		Version *int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.Version == nil || *in.Version < 0 {
		return nil, errInput
	}
	return s.changeProject(r, actor, *in.Version, nil, nil)
}

func (s *Service) changeProject(r *http.Request, actor identity.Principal, expected int64, in *profileInput, boardVersion *int64) (any, error) {
	k, ctx := scope(actor, r.PathValue("projectID")), r.Context()
	var version int64
	err := s.roles.WithOrganizationMutation(ctx, func(tx pgx.Tx) error {
		if err := s.requireSettingsMutation(ctx, tx, actor, k.project); err != nil {
			return err
		}
		prior, err := k.profile(ctx, tx)
		if err != nil {
			return err
		}
		if prior.Status == "archived" && in == nil && (expected == prior.Version || expected == prior.Version-1) {
			version = prior.Version
			return nil
		}
		if prior.Status == "archived" || prior.Version != expected {
			return errConflict
		}
		p := prior
		if in == nil {
			p.Status = "archived"
		} else {
			p, err = in.apply(p)
			if err != nil {
				return err
			}
			if in.Timezone != nil && p.Timezone != prior.Timezone {
				if err := k.setProfileTimezone(ctx, tx, p.Timezone, *boardVersion); err != nil {
					return err
				}
			}
		}
		p.Version++
		if err := k.saveProfile(ctx, tx, p); err != nil {
			return err
		}
		if p.Status != prior.Status && (p.Status == "archived" || p.Status == "paused") {
			if _, err := tx.Exec(ctx, `UPDATE project_relay_settings SET plan_enabled=false,version=version+1 WHERE `+projectWhere, k.args()...); err != nil {
				return err
			}
		}
		action := "project.profile.update"
		if p.Status == "archived" {
			action = "project.archive"
		}
		version = p.Version
		return k.auditProfile(ctx, tx, actor, action, version, map[string]string{
			"previous_status": prior.Status, "status": p.Status, "previous_project_type": prior.Type, "project_type": p.Type,
		})
	})
	return profileResult{ID: k.project, Version: version}, err
}

func (k taskScope) saveProfile(ctx context.Context, tx pgx.Tx, p Project) error {
	tag, err := tx.Exec(ctx, `INSERT INTO project_profiles
		(tenant_key,app_id,project_id,name,status,project_type,background,version,planned_start,planned_end)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,'')::date,nullif($10,'')::date)
		ON CONFLICT (tenant_key,app_id,project_id) DO UPDATE SET
		name=EXCLUDED.name,status=EXCLUDED.status,project_type=EXCLUDED.project_type,background=EXCLUDED.background,
		version=EXCLUDED.version,planned_start=EXCLUDED.planned_start,planned_end=EXCLUDED.planned_end
		WHERE project_profiles.version=EXCLUDED.version-1`,
		k.args(p.Name, p.Status, p.Type, p.Background, p.Version, p.PlannedStart, p.PlannedEnd)...)
	if err == nil && tag.RowsAffected() != 1 {
		return errConflict
	}
	return err
}

func (k taskScope) setProfileTimezone(ctx context.Context, tx pgx.Tx, timezone string, expected int64) error {
	query := `UPDATE project_task_boards SET business_timezone=$4,version=version+1 WHERE ` + projectWhere + ` AND version=$5`
	args := k.args(timezone, expected)
	if expected == 0 {
		query = `INSERT INTO project_task_boards (tenant_key,app_id,project_id,business_timezone)
			VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`
		args = k.args(timezone)
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return errConflict
	}
	return err
}

func (k taskScope) auditProfile(ctx context.Context, tx pgx.Tx, actor identity.Principal, action string, version int64, details map[string]string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version,details)
		VALUES($1,$2,$4,$5,'project',$3,'success',$6,$7)`, k.args(actor.AccountID, action, version, details)...)
	return err
}

func (k taskScope) relayStopped(ctx context.Context, db taskReader) (bool, error) {
	var stopped bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_profiles WHERE `+projectWhere+
		` AND status IN ('paused','archived'))`, k.args()...).Scan(&stopped)
	return stopped, err
}
