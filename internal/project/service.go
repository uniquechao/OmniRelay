package project

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/system"
)

type Bot interface {
	Bot(context.Context) (chat.Member, error)
	BotInGroup(context.Context, string) error
}

type Chat interface {
	ProjectGroup(context.Context, identity.Principal, string) (chat.Group, error)
	SendBriefing(context.Context, identity.Principal, string, string, chat.DeliveryContent, chat.BusinessReceiptKey, chat.BriefingAuthority) (chat.DeliveryStatus, error)
	BriefingDeliveryStatus(context.Context, identity.Principal, string, string) (chat.DeliveryStatus, error)
}

type Service struct {
	grid        *Grid
	pool        *pgxpool.Pool
	roles       *access.Service
	botStatus   func(context.Context) (system.Status, error)
	bot         Bot
	chat        Chat
	sendEnabled bool
}

func New(grid *Grid, pool *pgxpool.Pool, roles *access.Service, status func(context.Context) (system.Status, error), bot Bot, chats Chat, sendEnabled bool) *Service {
	return &Service{grid, pool, roles, status, bot, chats, sendEnabled}
}

func (s *Service) require(ctx context.Context, actor identity.Principal, action, id string) error {
	allowed, err := s.roles.Check(ctx, actor, action, id)
	if err != nil {
		return err
	}
	if !allowed {
		return errDenied
	}
	return nil
}

type settings struct {
	Target      string `json:"target_chat_id"`
	Timezone    string `json:"timezone"`
	Time        string `json:"local_time"`
	Enabled     bool   `json:"plan_enabled"`
	PublicCount bool   `json:"public_submission_count"`
	Version     int64  `json:"version"`
}

func (s *Service) settings(ctx context.Context, actor identity.Principal, id string) (settings, error) {
	cfg := settings{Time: "18:00"}
	err := s.pool.QueryRow(ctx, `SELECT coalesce(target_chat_id,''),timezone,local_time,plan_enabled,
		public_submission_count,version FROM project_relay_settings
		WHERE (tenant_key,app_id,project_id)=($1,$2,$3)`, actor.TenantKey, actor.AppID, id).
		Scan(&cfg.Target, &cfg.Timezone, &cfg.Time, &cfg.Enabled, &cfg.PublicCount, &cfg.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return cfg, err
}

func (s *Service) setSettings(ctx context.Context, actor identity.Principal, id string, cfg settings) (settings, error) {
	if _, err := time.LoadLocation(cfg.Timezone); err != nil || cfg.Timezone == "" || cfg.Timezone == "Local" ||
		((cfg.Enabled || cfg.PublicCount) && cfg.Target == "") || cfg.Version < 0 {
		return settings{}, errInput
	}
	hour, err := time.Parse("15:04", cfg.Time)
	if err != nil || hour.Format("15:04") != cfg.Time {
		return settings{}, errInput
	}
	if cfg.Target != "" {
		if !identifier.MatchString(cfg.Target) {
			return settings{}, errInput
		}
		if _, err := s.chat.ProjectGroup(ctx, actor, cfg.Target); err != nil {
			return settings{}, err
		}
	}
	if err := s.require(ctx, actor, "project.settings", id); err != nil {
		return settings{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		const columns = `tenant_key,app_id,project_id,target_chat_id,timezone,local_time,plan_enabled,public_submission_count,version`
		stmt := `UPDATE project_relay_settings SET target_chat_id=nullif($4,''),timezone=$5,local_time=$6,
			plan_enabled=$7,public_submission_count=$8,version=version+1
			WHERE (tenant_key,app_id,project_id,version)=($1,$2,$3,$9)
			AND EXISTS(SELECT 1 FROM accounts WHERE (tenant_key,app_id,id,open_id)=($1,$2,$10,$11) AND active)
			RETURNING version`
		if cfg.Version == 0 {
			stmt = `INSERT INTO project_relay_settings (` + columns + `)
				SELECT $1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9::bigint+1
				WHERE EXISTS(SELECT 1 FROM accounts WHERE (tenant_key,app_id,id,open_id)=($1,$2,$10,$11) AND active)
				ON CONFLICT (tenant_key,app_id,project_id) DO NOTHING RETURNING version`
		}
		err := tx.QueryRow(ctx, stmt, actor.TenantKey, actor.AppID, id, cfg.Target, cfg.Timezone,
			cfg.Time, cfg.Enabled, cfg.PublicCount, cfg.Version, actor.AccountID, actor.OpenID).Scan(&cfg.Version)
		var pgErr *pgconn.PgError
		if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "23505") {
			return errConflict
		}
		if err != nil {
			return err
		}
		if cfg.Target != "" {
			tag, err := tx.Exec(ctx, `INSERT INTO project_chat_links (tenant_key,app_id,project_id,chat_id)
				VALUES ($1,$2,$3,$4) ON CONFLICT (tenant_key,app_id,chat_id)
				DO UPDATE SET project_id=EXCLUDED.project_id WHERE project_chat_links.project_id=EXCLUDED.project_id`,
				actor.TenantKey, actor.AppID, id, cfg.Target)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errConflict
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(tenant_key,app_id,actor_account_id,action,resource_type,resource_id,result,change_version)
			VALUES ($1,$2,$3,'project.relay.configure','project',$4,'success',$5)`,
			actor.TenantKey, actor.AppID, actor.AccountID, id, cfg.Version)
		return err
	})
	return cfg, err
}

type checkInput struct {
	Account string `json:"account_id"`
	Action  string `json:"action"`
	Scope   string `json:"public_scope"`
}

type checkResult struct {
	Bot     string `json:"bot"`
	Group   string `json:"group"`
	Account string `json:"account"`
	Action  string `json:"action"`
	Scope   string `json:"public_scope"`
	Passed  bool   `json:"passed"`
}

func (s *Service) check(ctx context.Context, actor identity.Principal, id string, input checkInput) (checkResult, error) {
	result := checkResult{Bot: "unavailable", Group: "not_checked", Account: "not_checked", Action: "not_checked", Scope: "not_checked"}
	if input.Account != "" && !identifier.MatchString(input.Account) {
		return result, errInput
	}
	if input.Account != "" && input.Account != actor.AccountID {
		if err := s.require(ctx, actor, "account.read", ""); err != nil {
			return result, err
		}
	}
	switch input.Action {
	case "project.read", "project.assign", "project.settings":
	default:
		return result, errInput
	}
	if input.Scope != "project_only" && input.Scope != "submission_count" {
		return result, errInput
	}
	cfg, err := s.settings(ctx, actor, id)
	if err != nil {
		return result, err
	}
	result.Bot = s.checkBot(ctx)
	if cfg.Target == "" {
		result.Group = "not_bound"
	} else if result.Bot == "available" {
		switch err := s.bot.BotInGroup(ctx, cfg.Target); {
		case err == nil:
			result.Group = "bot_member"
		case errors.Is(err, chat.ErrAccessLost):
			result.Group = "bot_not_member"
		default:
			result.Group = "unavailable"
		}
	}
	subject := actor
	account := input.Account
	if account == "" {
		account = actor.AccountID
	}
	err = s.pool.QueryRow(ctx, `SELECT id,open_id FROM accounts
		WHERE (tenant_key,app_id,id)=($1,$2,$3) AND active`, actor.TenantKey, actor.AppID, account).
		Scan(&subject.AccountID, &subject.OpenID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result.Account = "inactive_or_missing"
	case err != nil:
		return result, err
	default:
		result.Account = "active"
		_, err = s.grid.credential(ctx, subject)
		switch {
		case errors.Is(err, errUnmapped):
			result.Account = "identity_unavailable"
		case err != nil:
			result.Account = "unavailable"
		default:
			switch err := s.require(ctx, subject, input.Action, id); {
			case err == nil:
				result.Action = "allowed"
			case errors.Is(err, errDenied):
				result.Action = "denied"
			default:
				result.Action = "unavailable"
			}
		}
	}
	if result.Group == "bot_member" && result.Account == "active" {
		switch _, err := s.chat.ProjectGroup(ctx, subject, cfg.Target); {
		case err == nil:
			result.Group = "member"
		case errors.Is(err, chat.ErrAccessLost):
			result.Group = "account_source_denied"
		default:
			result.Group = "unavailable"
		}
	}
	switch {
	case input.Scope == "project_only":
		result.Scope = "project_only"
	case cfg.Target == "" || !cfg.PublicCount:
		result.Scope = "denied"
	case result.Group == "member" && result.Action == "allowed":
		result.Scope = "allowed"
	}
	current, err := s.settings(ctx, actor, id)
	if err != nil {
		return result, err
	}
	if current.Version != cfg.Version {
		return result, errConflict
	}
	result.Passed = result.Bot == "available" && result.Group == "member" && result.Account == "active" &&
		result.Action == "allowed" && (result.Scope == "allowed" || result.Scope == "project_only")
	return result, nil
}

func (s *Service) checkBot(ctx context.Context) string {
	status, err := s.botStatus(ctx)
	switch {
	case err != nil:
		return "unavailable"
	case !status.CredentialsConfigured:
		return "not_configured"
	case !status.Enabled:
		return "disabled"
	}
	if _, err := s.bot.Bot(ctx); err != nil {
		return "unavailable"
	}
	return "available"
}
