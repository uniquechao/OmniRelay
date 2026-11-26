package group

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

type NoticeTarget struct {
	Mode   Mode
	Target string
}

var errNoticeConflict = errors.New("different notice with same request key")

type NoticeSpec struct {
	Key              string
	Project, Channel string
	Config           int64
	At               time.Time
	Targets          []NoticeTarget
}

func authorizeRow(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrForbidden
	}
	return err
}

// CreateNoticePlan is the trusted application entry point. Runtime certificates
// cannot create grants or choose recipients. Each plan covers a single UTC instant.
func (s *Service) CreateNoticePlan(ctx context.Context, actor identity.Principal, spec NoticeSpec) (string, error) {
	adapter, ok := s.Channels[spec.Channel]
	if !ok || actor.AppID != s.App || actor.TenantKey != s.Tenant || len(spec.Targets) == 0 || spec.At.IsZero() || spec.At.Nanosecond() != 0 {
		return "", access.ErrForbidden
	}
	spec.At = spec.At.UTC()
	spec.Targets = slices.Clone(spec.Targets)
	slices.SortFunc(spec.Targets, func(a, b NoticeTarget) int {
		if c := cmp.Compare(a.Mode, b.Mode); c != 0 {
			return c
		}
		return cmp.Compare(a.Target, b.Target)
	})
	for i, x := range spec.Targets {
		if !x.Mode.notice() || x.Target == "" || (i > 0 && spec.Targets[i-1] == x) {
			return "", access.ErrForbidden
		}
	}
	definition, _ := json.Marshal(struct {
		At      time.Time
		Channel string
		Targets []NoticeTarget
	}{spec.At, spec.Channel, spec.Targets})
	var id string
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		check := func(ctx context.Context, p identity.Principal, action, project string) (bool, error) {
			return s.Roles.CheckInOrganizationMutation(ctx, tx, p, action, project)
		}
		allowed, err := check(ctx, actor, "project.settings", spec.Project)
		if err != nil {
			return err
		}
		if !allowed {
			return access.ErrForbidden
		}
		agent, err := (routing.Gateway{Check: check}).Resolve(ctx, actor, "project", spec.Project)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_instances(agent_id,tenant_key,app_id,project_id,owner_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, agent.AgentID, s.Tenant, s.App, spec.Project, s.Owner)
		if err != nil {
			return err
		}
		if spec.Key != "" {
			var same bool
			err = tx.QueryRow(ctx, "SELECT id::text,definition=$4::jsonb FROM agent_notice_plans WHERE agent_id=$1 AND creator=$2 AND request_key=$3", agent.AgentID, actor.AccountID, spec.Key, definition).Scan(&id, &same)
			if err == nil {
				if !same {
					return errNoticeConflict
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var frozenBot string
		for _, target := range spec.Targets {
			subject := actor.AccountID
			if target.Mode == PersonNotice {
				subject = target.Target
			}
			address := Envelope{Tenant: s.Tenant, App: s.App, Channel: spec.Channel, Project: spec.Project, Mode: target.Mode, Config: spec.Config, Chat: target.Target, Account: subject}
			resolved, err := adapter.Resolve(ctx, tx, address)
			if err != nil {
				return err
			}
			if resolved.Project != spec.Project || resolved.Bot == "" || resolved.Recipient == "" {
				return access.ErrForbidden
			}
			if id == "" {
				frozenBot = resolved.Bot
				err = tx.QueryRow(ctx, `INSERT INTO agent_notice_plans(agent_id,creator,creator_generation,creator_permissions,channel,bot,config_version,due_at,request_key,definition)
             SELECT $1,a.id,m.generation,a.permission_version,$2,$3,$4,$5,coalesce(nullif($8,''),gen_random_uuid()::text),$9::jsonb FROM agent_instances i
             JOIN project_members m USING(tenant_key,app_id,project_id)
             JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
             WHERE i.agent_id=$1 AND i.owner_id=$6 AND a.id=$7 AND a.active AND m.role!='revoked' AND $5>now() RETURNING id::text`,
					agent.AgentID, spec.Channel, frozenBot, spec.Config, spec.At, s.Owner, actor.AccountID, spec.Key, definition).Scan(&id)
				if err != nil {
					return authorizeRow(err)
				}
			}
			if resolved.Bot != frozenBot {
				return access.ErrForbidden
			}
			tag, err := tx.Exec(ctx, `INSERT INTO agent_notice_targets(plan_id,mode,target,recipient,assignment_version,membership_generation,permission_version)
           SELECT $1,$2,$3,$4,$5,m.generation,a.permission_version FROM project_members m
           JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(m.tenant_key,m.app_id,m.account_id)
           WHERE m.tenant_key=$6 AND m.app_id=$7 AND m.project_id=$8 AND a.id=$9 AND a.active AND m.role!='revoked'`,
				id, target.Mode, target.Target, resolved.Recipient, resolved.Version, s.Tenant, s.App, spec.Project, subject)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return access.ErrForbidden
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) checkNotice(ctx context.Context, tx pgx.Tx, plan string, e Envelope, a Assignment) error {
	var actor identity.Principal
	actor.TenantKey, actor.AppID = s.Tenant, s.App
	err := tx.QueryRow(ctx, `SELECT p.creator,coalesce(u.open_id,'') FROM agent_notice_plans p JOIN agent_instances i USING(agent_id)
 JOIN agent_notice_targets t ON t.plan_id=p.id
 JOIN project_members m ON (m.tenant_key,m.app_id,m.project_id,m.account_id)=(i.tenant_key,i.app_id,i.project_id,p.creator)
 JOIN accounts u ON (u.tenant_key,u.app_id,u.id)=(i.tenant_key,i.app_id,p.creator)
 WHERE p.id=$1 AND p.enabled AND i.tenant_key=$2 AND i.app_id=$3 AND i.project_id=$4 AND p.channel=$5 AND p.bot=$6
 AND p.config_version=$7 AND t.mode=$8 AND t.target=$9 AND t.recipient=$10 AND t.assignment_version=$11 AND
 u.active AND u.permission_version=p.creator_permissions AND m.generation=p.creator_generation AND m.role!='revoked'`,
		plan, s.Tenant, s.App, e.Project, e.Channel, a.Bot, e.Config, e.Mode, e.Chat, a.Recipient, a.Version).Scan(&actor.AccountID, &actor.OpenID)
	if err != nil {
		return authorizeRow(err)
	}
	allowed, err := s.Roles.CheckInOrganizationMutation(ctx, tx, actor, "project.settings", e.Project)
	if err == nil && !allowed {
		return access.ErrForbidden
	}
	return err
}

type scheduleEvent struct {
	Plan   string    `json:"plan"`
	Native string    `json:"native,omitempty"`
	At     time.Time `json:"at,omitempty"`
	Epoch  int64     `json:"epoch,omitempty"`
}
type clockBinding struct {
	Agent  string    `json:"agent"`
	Epoch  int64     `json:"epoch"`
	At     time.Time `json:"at"`
	Native string    `json:"native"`
}

func (s *Service) scheduleEvent(ctx context.Context, action string, e scheduleEvent) (clockBinding, error) {
	var b clockBinding
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT i.agent_id,i.owner_epoch,p.due_at,coalesce(p.native_id,'') FROM agent_notice_plans p JOIN agent_instances i USING(agent_id)
       WHERE p.id::text=$1 AND i.owner_id=$2 AND i.tenant_key=$3 AND i.app_id=$4 AND p.enabled AND p.due_at+interval '10 minutes'>clock_timestamp()
       FOR UPDATE OF p FOR SHARE OF i`, e.Plan, s.Owner, s.Tenant, s.App).Scan(&b.Agent, &b.Epoch, &b.At, &b.Native)
		if err != nil {
			return authorizeRow(err)
		}
		if action == "prepare" {
			return nil
		}
		if e.Native == "" || len(e.Native) > 128 || e.Epoch != b.Epoch || !b.At.Equal(e.At) {
			return access.ErrForbidden
		}
		if action == "bind" {
			if b.Native != "" && b.Native != e.Native {
				return access.ErrForbidden
			}
			_, err = tx.Exec(ctx, "UPDATE agent_notice_plans SET native_id=$2 WHERE id=$1", e.Plan, e.Native)
			return err
		}
		var due bool
		if err = tx.QueryRow(ctx, "SELECT clock_timestamp()>=$1", b.At).Scan(&due); err != nil {
			return err
		}
		if !due || b.Native != e.Native {
			return access.ErrForbidden
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_group_work(tenant_key,app_id,channel,account_id,chat_id,thread_id,message_id,config_version,project_id,
        membership_generation,assignment_version,permission_version,deadline,mode,plan_id,agent_id)
      SELECT i.tenant_key,i.app_id,p.channel,CASE t.mode WHEN 'person_notice' THEN t.target ELSE p.creator END,t.target,'','',p.config_version,i.project_id,
        t.membership_generation,t.assignment_version,t.permission_version,p.due_at+interval '10 minutes',t.mode,p.id,i.agent_id
      FROM agent_notice_plans p JOIN agent_instances i USING(agent_id) JOIN agent_notice_targets t ON p.id=t.plan_id WHERE p.id=$1
      ON CONFLICT(plan_id,mode,chat_id) WHERE plan_id IS NOT NULL DO NOTHING`, e.Plan)
		return err
	})
	return b, err
}

func (s *Service) registerSchedule(mux *http.ServeMux) {
	// Discovery assigns coordinator homes to their owner. Only Harness fires timers.
	mux.HandleFunc("GET /internal/schedules", func(w http.ResponseWriter, r *http.Request) {
		if !s.trustedRuntime(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		rows, err := s.DB.Query(r.Context(), `SELECT p.id::text FROM agent_notice_plans p JOIN agent_instances i USING(agent_id)
           WHERE i.owner_id=$1 AND i.tenant_key=$2 AND i.app_id=$3 AND p.enabled
           AND p.due_at+interval '10 minutes'>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM agent_group_work w WHERE w.plan_id=p.id)
           ORDER BY p.due_at,p.id LIMIT 4`, s.Owner, s.Tenant, s.App)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ids)
	})

	for _, action := range []string{"prepare", "bind", "occur"} {
		mux.HandleFunc("POST /internal/schedules/"+action, func(w http.ResponseWriter, r *http.Request) {
			if !s.trustedRuntime(r) {
				http.Error(w, "forbidden", 403)
				return
			}
			var e scheduleEvent
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			d.DisallowUnknownFields()
			if d.Decode(&e) != nil || d.Decode(&struct{}{}) != io.EOF {
				http.Error(w, "invalid event", 400)
				return
			}
			b, err := s.scheduleEvent(r.Context(), action, e)
			if errors.Is(err, access.ErrForbidden) {
				http.Error(w, "forbidden", 403)
				return
			}
			if err != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(b)
		})
	}
}
