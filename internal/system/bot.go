// Package system owns service connection settings and Feishu Bot management.
package system

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var (
	ErrVersionConflict = errors.New("bot configuration version conflict")
	ErrKeyConflict     = errors.New("bot check idempotency conflict")
	ErrCheckNotFound   = errors.New("bot check not found")
	ErrStaleGeneration = errors.New("bot credentials generation is outdated")
)

type Config struct {
	AppID                 string `json:"app_id"`
	TenantKey             string `json:"tenant_key"`
	CallbackURL           string `json:"oauth_callback_url"`
	CredentialsConfigured bool   `json:"credentials_configured"`
}

type Status struct {
	Config
	BotName    string     `json:"bot_name"`
	Enabled    bool       `json:"enabled"`
	Version    int64      `json:"version"`
	Connection Connection `json:"connection"`
}

func (s Status) ETag() string { return `"bot-` + strconv.FormatInt(s.Version, 10) + `"` }

type Connection struct {
	State           string     `json:"state"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	LastMessageAt   *time.Time `json:"last_message_at"`
}

type Check struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Generation string     `json:"credentials_generation"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Results    []Result   `json:"results"`
}

type Result struct {
	Name      string     `json:"name"`
	State     string     `json:"state"`
	CheckedAt time.Time  `json:"checked_at"`
	Error     CheckError `json:"error,omitempty"`
}

type Claim struct {
	ID      string
	Version int64
}

type Submission struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Reused bool   `json:"reused"`
}

type BotInfo struct {
	Name, OpenID string
}

type CheckError string

const (
	tenantTokenCheck = "tenant_token"
	botInfoCheck     = "bot_info"

	CredentialsMissing  CheckError = "credentials_missing"
	CredentialsRejected CheckError = "credentials_rejected"
	BotUnavailable      CheckError = "bot_unavailable"
	ProviderUnavailable CheckError = "provider_temporarily_unavailable"
)

type Provider interface {
	TenantToken(context.Context) (string, CheckError)
	BotInfo(context.Context, string) (BotInfo, CheckError)
}

type Store interface {
	Status(context.Context) (Status, error)
	Configure(context.Context, identity.Principal, string, bool) (Status, error)
	Submit(context.Context, identity.Principal, string) (Submission, error)
	Check(context.Context, string) (Check, error)
	Claim(context.Context) (Claim, error)
	Renew(context.Context, Claim, Result) (time.Time, error)
	Finish(context.Context, Claim, []Result, BotInfo) error
}

type Permissions interface {
	Check(context.Context, identity.Principal, string, string) (bool, error)
}

type AuthorizeWrite func(context.Context, pgx.Tx, identity.Principal, string) error
type AuditChange func(context.Context, pgx.Tx, identity.Principal, int64) error

type Service struct {
	store    Store
	provider Provider
	wake     chan struct{}
}

func New(store Store, provider Provider) *Service {
	return &Service{store: store, provider: provider, wake: make(chan struct{}, 1)}
}

func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
		claim, err := s.store.Claim(attempt)
		if err == nil && claim.ID != "" {
			s.perform(attempt, claim)
		}
		cancel()
		if errors.Is(err, ErrStaleGeneration) {
			return
		}
		if err == nil && claim.ID != "" {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (s *Service) perform(ctx context.Context, claim Claim) {
	token, failure := s.provider.TenantToken(ctx)
	first := result(tenantTokenCheck, failure)
	if ctx.Err() != nil {
		return
	}
	checkedAt, err := s.store.Renew(ctx, claim, first)
	if err != nil {
		return
	}
	first.CheckedAt = checkedAt
	info := BotInfo{}
	second := Result{Name: botInfoCheck, State: "skipped"}
	if failure == "" {
		info, failure = s.provider.BotInfo(ctx, token)
		second = result(botInfoCheck, failure)
	}
	if ctx.Err() == nil {
		_ = s.store.Finish(ctx, claim, []Result{first, second}, info)
	}
}

func result(name string, failure CheckError) Result {
	if failure == "" {
		return Result{Name: name, State: "passed"}
	}
	switch failure {
	case CredentialsMissing, CredentialsRejected, BotUnavailable, ProviderUnavailable:
	default:
		failure = ProviderUnavailable
	}
	return Result{Name: name, State: "failed", Error: failure}
}
