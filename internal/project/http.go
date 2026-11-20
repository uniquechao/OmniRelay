package project

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func (s *Service) Register(mux *http.ServeMux, auth func(http.ResponseWriter, *http.Request, identity.SessionPolicy) (identity.Principal, bool)) {
	list := func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		filter := r.URL.Query()
		page, size := 1, 25
		for key, values := range filter {
			if len(values) != 1 || len(values[0]) > 200 {
				return nil, errInput
			}
			switch key {
			case "q", "status":
				continue
			case "page", "page_size":
				number, err := strconv.Atoi(values[0])
				if err != nil || number < 1 || number > 100_000 || (key == "page_size" && number > 100) {
					return nil, errInput
				}
				if key == "page" {
					page = number
				} else {
					size = number
				}
				filter.Del(key)
			default:
				return nil, errInput
			}
		}
		result, err := s.grid.directory(r.Context(), actor, filter)
		if err != nil {
			return nil, err
		}
		start := min((page-1)*size, result.Total)
		result.Items = result.Items[start:min(start+size, result.Total)]
		result.Page, result.PageSize = page, size
		return result, nil
	}
	handle := func(pattern, action string, next func(http.ResponseWriter, *http.Request, identity.Principal) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			policy := identity.ValidateSession
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Pattern != "POST /api/v1/projects/{projectID}/feishu/check" {
				policy = identity.RefreshSessionWithCSRF
			}
			actor, ok := auth(w, r, policy)
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			id := r.PathValue("projectID")
			section, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"+id+"/"), "/")
			if s.grid == nil && (id == "" || section != "tasks" && section != "milestones" && section != "overview") {
				failure(w, errUnavailable)
				return
			}
			if id != "" {
				if !identifier.MatchString(id) {
					failure(w, errDenied)
					return
				}
				if err := s.require(ctx, actor, action, id); err != nil {
					failure(w, err)
					return
				}
			}
			value, err := next(w, r, actor)
			if err != nil {
				failure(w, err)
				return
			}
			if id != "" && policy == identity.ValidateSession {
				if err := s.require(ctx, actor, action, id); err != nil {
					failure(w, err)
					return
				}
			}
			writeJSON(w, http.StatusOK, value)
		})
	}
	const base = "/api/v1/projects"
	const project = base + "/{projectID}"
	handle("GET "+base, "", list)
	for panel := range endpoints {
		handle("GET "+project+"/"+panel, "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
			query, err := viewQuery(panel, r.URL.Query())
			if err != nil {
				return nil, err
			}
			id := r.PathValue("projectID")
			if panel != "overview" {
				return s.grid.view(r.Context(), actor, id, panel, query)
			}
			local, err := s.milestones(r.Context(), scope(actor, id), time.Now())
			if err != nil {
				return nil, err
			}
			if s.grid == nil {
				return map[string]any{"project_id": id, "panel": panel, "data": map[string]any{"workspace": map[string]any{"project": map[string]string{"id": id}}, "milestones": map[string]any{"items": local}, "coverage": map[string]string{"state": "partial"}}}, nil
			}
			view, err := s.grid.view(r.Context(), actor, id, panel, query)
			if err != nil {
				return nil, err
			}
			view.Data, err = overviewWithMilestones(view.Data, local)
			return view, err
		})
	}
	handle("GET "+project+"/tasks", "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		query, err := viewQuery("tasks", r.URL.Query())
		if err != nil {
			return nil, err
		}
		board, err := scope(actor, r.PathValue("projectID")).board(r.Context(), s.pool, query, time.Now())
		if err != nil {
			return nil, err
		}
		board.CanMove, err = s.roles.Check(r.Context(), actor, "project.assign", board.Project)
		if err != nil {
			return nil, err
		}
		return map[string]any{"project_id": board.Project, "panel": "tasks", "data": board, "total": board.Total}, nil
	})
	handle("POST "+project+"/tasks", "project.assign", s.saveTask)
	handle("PUT "+project+"/tasks/{taskID}", "project.assign", s.saveTask)
	handle("PATCH "+project+"/tasks/{taskID}/milestone", "project.assign", s.setTaskMilestone)
	handle("PUT "+project+"/tasks/board/timezone", "project.settings", s.setBoardTimezone)
	handle("POST "+project+"/tasks/{taskID}/move", "project.assign", s.moveTask)
	handle("GET "+project+"/milestones", "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		items, err := s.milestones(r.Context(), scope(actor, r.PathValue("projectID")), time.Now())
		return map[string]any{"items": items}, err
	})
	handle("POST "+project+"/milestones", "project.assign", s.createMilestone)
	handle("GET "+project+"/tasks/{taskID}", "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		return scope(actor, r.PathValue("projectID")).task(r.Context(), s.pool, r.PathValue("taskID"), time.Now())
	})
	handle("GET "+project+"/sources/{kind}/{sourceID}/tasks", "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		id, kind, chatID := r.PathValue("projectID"), r.PathValue("kind"), r.URL.Query().Get("chat_id")
		if kind == "task" {
			return nil, errInput
		}
		var groupVersion int64
		if kind == "message" {
			group, err := assignment(r.Context(), s.pool, actor, chatID)
			if err != nil {
				return nil, err
			}
			if group.ProjectID != id {
				return nil, errDenied
			}
			groupVersion = group.Version
			if _, err := s.chat.ProjectGroup(r.Context(), actor, chatID); err != nil {
				return nil, err
			}
		}
		value, err := s.grid.record(r.Context(), actor, id, kind, r.PathValue("sourceID"), chatID, func(taskID string) (storedTask, error) {
			return scope(actor, id).task(r.Context(), s.pool, taskID, time.Now())
		})
		if err != nil {
			return nil, err
		}
		if kind == "message" {
			group, err := assignment(r.Context(), s.pool, actor, chatID)
			if err != nil {
				return nil, err
			}
			if group.ProjectID != id || group.Version != groupVersion {
				return nil, errDenied
			}
		}
		return value, nil
	})
	handle("GET "+project+"/reports/briefings/{runID}", "project.read", s.latestBriefing)
	handle("GET "+project+"/chats", "project.read", s.getChats)
	handle("PUT /api/v1/feishu/chats/{chatID}/project", "", func(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		var input struct {
			ProjectID *string `json:"project_id"`
			Version   *int64  `json:"version"`
		}
		if !decode(w, r, &input) || input.ProjectID == nil || input.Version == nil {
			return nil, errInput
		}
		return s.setAssignment(r.Context(), actor, r.PathValue("chatID"), *input.ProjectID, *input.Version)
	})
	handle("PUT "+project+"/chats/primary", "project.settings", func(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		var input struct {
			ChatID            string `json:"chat_id"`
			AssignmentVersion *int64 `json:"assignment_version"`
			PrimaryVersion    *int64 `json:"primary_version"`
		}
		if !decode(w, r, &input) || input.AssignmentVersion == nil || input.PrimaryVersion == nil {
			return nil, errInput
		}
		return s.setPrimary(r.Context(), actor, r.PathValue("projectID"), input.ChatID, *input.AssignmentVersion, *input.PrimaryVersion)
	})
	handle("GET "+project+"/feishu", "project.read", s.getBot)
	handle("PUT "+project+"/feishu/settings", "project.settings", func(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		var input settings
		if !decode(w, r, &input) {
			return nil, errInput
		}
		return s.setSettings(r.Context(), actor, r.PathValue("projectID"), input)
	})
	handle("POST "+project+"/feishu/check", "project.settings", func(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		var input checkInput
		if !decode(w, r, &input) {
			return nil, errInput
		}
		return s.check(r.Context(), actor, r.PathValue("projectID"), input)
	})
	handle("GET "+project+"/reports/briefings", "project.read", s.latestBriefing)
	handle("POST "+project+"/reports/briefings", "project.settings", func(w http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		var input struct {
			Date string `json:"business_date"`
		}
		if !decode(w, r, &input) {
			return nil, errInput
		}
		return s.generate(r.Context(), actor, r.PathValue("projectID"), input.Date)
	})
	handle("POST "+project+"/reports/briefings/{runID}/publish", "project.settings", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		return s.publish(r.Context(), actor, r.PathValue("projectID"), r.PathValue("runID"))
	})
	handle("POST "+project+"/reports/briefings/{runID}/reconcile", "project.read", func(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
		id := r.PathValue("projectID")
		run, err := s.run(r.Context(), actor, id, r.PathValue("runID"))
		if err != nil {
			return nil, err
		}
		if run.Target == "" || run.Delivery == "not_sent" {
			return nil, errDenied
		}
		return s.chat.BriefingDeliveryStatus(r.Context(), actor, run.Target, run.receipt(id).OperationID)
	})
}

func (s *Service) sameSettings(ctx context.Context, actor identity.Principal, id string, prior settings) (bool, error) {
	current, err := s.settings(ctx, actor, id)
	return current.Version == prior.Version, err
}

func (s *Service) getBot(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	ctx, id := r.Context(), r.PathValue("projectID")
	cfg, err := s.settings(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	status := "not_bound"
	var targetName string
	if cfg.Target != "" {
		group, err := s.chat.ProjectGroup(ctx, actor, cfg.Target)
		if errors.Is(err, chat.ErrAccessLost) {
			return map[string]any{"project_id": id, "state": "source_denied"}, nil
		}
		if err != nil {
			return nil, err
		}
		targetName = group.Name
		status = s.checkBot(ctx)
	}
	receipts, err := s.latestBriefing(nil, r, actor)
	if err != nil {
		return nil, err
	}
	ok, err := s.sameSettings(ctx, actor, id, cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errConflict
	}
	canCheck, err := s.roles.Check(ctx, actor, "project.settings", id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project_id": id, "state": status, "target_name": targetName, "can_check": canCheck, "plan": cfg, "briefings": receipts}, nil
}

func (s *Service) latestBriefing(_ http.ResponseWriter, r *http.Request, actor identity.Principal) (any, error) {
	ctx, id := r.Context(), r.PathValue("projectID")
	runID := r.PathValue("runID")
	var err error
	if runID == "" {
		err = s.pool.QueryRow(ctx, `SELECT id FROM project_briefing_runs
            WHERE (tenant_key,app_id,project_id,account_id)=($1,$2,$3,$4)
            ORDER BY business_date DESC LIMIT 1`, actor.TenantKey, actor.AppID, id, actor.AccountID).Scan(&runID)
	} else if !identifier.MatchString(runID) {
		return nil, errInput
	}
	items := []briefing{}
	if err == nil {
		run, err := s.authorizedRun(ctx, actor, id, runID)
		if r.PathValue("runID") == "" && (errors.Is(err, errConflict) || errors.Is(err, errDenied)) {
			return map[string]any{"items": items, "total": 0}, nil
		}
		if err != nil {
			return nil, err
		}
		if run.Target != "" {
			if _, err := s.chat.ProjectGroup(ctx, actor, run.Target); err != nil {
				if !errors.Is(err, chat.ErrAccessLost) {
					return nil, err
				}
				run.Target, run.Delivery, run.ConfirmedAt = "", "withheld", nil
			}
		}
		items = append(items, run)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeLimit(w, r, dst, 4096)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	return media == "application/json" && d.Decode(dst) == nil && d.Decode(new(json.RawMessage)) == io.EOF
}

func failure(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "project_unavailable"
	switch {
	case errors.Is(err, errDenied), errors.Is(err, errUnmapped), errors.Is(err, chat.ErrAccessLost):
		status, code = http.StatusNotFound, "project_access_denied"
	case errors.Is(err, errInput):
		status, code = http.StatusBadRequest, "invalid_project_request"
	case errors.Is(err, errTaskVersion):
		status, code = http.StatusConflict, "task_version_conflict"
	case errors.Is(err, errBoardVersion):
		status, code = http.StatusConflict, "board_version_conflict"
	case errors.Is(err, errMoveKey):
		status, code = http.StatusConflict, "move_request_key_conflict"
	case errors.Is(err, errConflict):
		status, code = http.StatusConflict, "project_state_changed"
	}
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
