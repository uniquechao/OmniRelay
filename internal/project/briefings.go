package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

type briefing struct {
	ID           string     `json:"id"`
	Date         string     `json:"business_date"`
	Target       string     `json:"target_chat_id,omitempty"`
	VisibleCount int        `json:"visible_submissions"`
	GeneratedAt  time.Time  `json:"generated_at"`
	Delivery     string     `json:"delivery_state"`
	ConfirmedAt  *time.Time `json:"delivery_confirmed_at,omitempty"`
	version      int64
	revision     string
}

func (s *Service) submissionCount(ctx context.Context, actor identity.Principal, id, date string) (int, string, error) {
	view, err := s.grid.view(ctx, actor, id, "reports", url.Values{
		"source": {"member"}, "submission_state": {"submitted"}, "date_from": {date}, "date_to": {date}, "page_size": {"1"},
	})
	if err != nil {
		return 0, "", err
	}
	var payload struct{ Revision string }
	if json.Unmarshal(view.Data, &payload) != nil || payload.Revision == "" || view.Total == nil {
		return 0, "", errUnavailable
	}
	return *view.Total, payload.Revision, nil
}

func (s *Service) generate(ctx context.Context, actor identity.Principal, id, date string) (briefing, error) {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil || day.Year() < 1 || day.Format(time.DateOnly) != date {
		return briefing{}, errInput
	}
	cfg, err := s.settings(ctx, actor, id)
	if err != nil {
		return briefing{}, err
	}
	count, revision, err := s.submissionCount(ctx, actor, id, date)
	if err != nil {
		return briefing{}, err
	}
	if err := s.require(ctx, actor, "project.settings", id); err != nil {
		return briefing{}, err
	}
	var run briefing
	err = s.pool.QueryRow(ctx, `INSERT INTO project_briefing_runs
		(tenant_key,app_id,project_id,business_date,account_id,target_chat_id,settings_version,source_revision,visible_submissions)
		SELECT $1,$2,$3,$4::text::date,$5,nullif($6,''),$7,$8,$9
		WHERE EXISTS(SELECT 1 FROM accounts WHERE (tenant_key,app_id,id,open_id)=($1,$2,$5,$10) AND active)
		AND coalesce((SELECT version FROM project_relay_settings WHERE (tenant_key,app_id,project_id)=($1,$2,$3)),0)=$7
		ON CONFLICT (tenant_key,app_id,project_id,business_date) DO NOTHING
		RETURNING id,business_date::text,generated_at,visible_submissions`,
		actor.TenantKey, actor.AppID, id, date, actor.AccountID, cfg.Target, cfg.Version, revision, count, actor.OpenID).
		Scan(&run.ID, &run.Date, &run.GeneratedAt, &run.VisibleCount)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same authorized generation can be retried after a lost response.
		err = s.pool.QueryRow(ctx, `SELECT id,business_date::text,generated_at,visible_submissions
			FROM project_briefing_runs r
			WHERE (r.tenant_key,r.app_id,r.project_id,r.business_date,r.account_id)=($1,$2,$3,$4::text::date,$5)
			AND (r.settings_version,r.source_revision,r.visible_submissions)=($6,$7,$8)
			AND NOT EXISTS (SELECT 1 FROM feishu_send_operations o
				WHERE (o.tenant_key,o.app_id,o.operation_key)=(r.tenant_key,r.app_id,'briefing:'||r.id))`,
			actor.TenantKey, actor.AppID, id, date, actor.AccountID, cfg.Version, revision, count).
			Scan(&run.ID, &run.Date, &run.GeneratedAt, &run.VisibleCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return briefing{}, errConflict
		}
	}
	// Generation returns no destination or delivery claim until separately authorized.
	run.Delivery = "not_sent"
	return run, err
}

func (s *Service) run(ctx context.Context, actor identity.Principal, projectID, runID string) (briefing, error) {
	var run briefing
	err := s.pool.QueryRow(ctx, `SELECT r.id,r.business_date::text,coalesce(r.target_chat_id,''),r.visible_submissions,
		r.generated_at,r.settings_version,r.source_revision,
		CASE WHEN p.state='pending' AND o.deadline<=now() THEN CASE WHEN p.attempted THEN 'unknown' ELSE 'failed' END
		     ELSE coalesce(p.state,'not_sent') END,p.confirmed_at
		FROM project_briefing_runs r
		LEFT JOIN feishu_send_operations o ON (o.tenant_key,o.app_id,o.operation_key,o.account_id,o.chat_id)=
			(r.tenant_key,r.app_id,'briefing:'||r.id,r.account_id,r.target_chat_id) AND o.business_receipt IS NOT NULL
		LEFT JOIN feishu_send_parts p ON (p.tenant_key,p.app_id,p.operation_key,p.position)=(o.tenant_key,o.app_id,o.operation_key,0)
		WHERE (r.tenant_key,r.app_id,r.project_id,r.id,r.account_id)=($1,$2,$3,$4,$5)`,
		actor.TenantKey, actor.AppID, projectID, runID, actor.AccountID).
		Scan(&run.ID, &run.Date, &run.Target, &run.VisibleCount, &run.GeneratedAt, &run.version, &run.revision, &run.Delivery, &run.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return briefing{}, errDenied
	}
	return run, err
}

func (s *Service) authorizedRun(ctx context.Context, actor identity.Principal, projectID, runID string) (briefing, error) {
	run, err := s.run(ctx, actor, projectID, runID)
	if err != nil {
		return briefing{}, err
	}
	count, revision, err := s.submissionCount(ctx, actor, projectID, run.Date)
	if err != nil {
		return briefing{}, err
	}
	if revision != run.revision || count != run.VisibleCount {
		return briefing{}, errConflict
	}
	return run, nil
}

func (run briefing) receipt(projectID string) chat.BusinessReceiptKey {
	return chat.BusinessReceiptKey{
		OperationID: "briefing:" + run.ID,
		Target:      chat.ReceiptTarget{ReceiptID: run.ID, ProjectID: projectID, TargetKind: "briefing", TargetID: run.ID},
	}
}

func (run briefing) content() chat.DeliveryContent {
	return chat.DeliveryContent{Text: fmt.Sprintf("项目简报 · %s\n本次有权汇总的已提交成员日报：%d", run.Date, run.VisibleCount)}
}

func (s *Service) authorizeSend(ctx context.Context, actor identity.Principal, key chat.BusinessReceiptKey, target string, content chat.DeliveryContent) (bool, error) {
	id := key.Target.ProjectID
	if err := s.require(ctx, actor, "project.settings", id); err != nil {
		return false, err
	}
	run, err := s.authorizedRun(ctx, actor, id, key.Target.TargetID)
	if err != nil {
		return false, err
	}
	if key != run.receipt(id) || target == "" || target != run.Target || content.Text != run.content().Text ||
		len(content.Paragraphs) != 0 || len(content.Attachments) != 0 {
		return false, nil
	}
	if _, err := s.chat.ProjectGroup(ctx, actor, target); err != nil {
		return false, err
	}
	var allowed bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM project_relay_settings r JOIN project_chat_links l
		ON (l.tenant_key,l.app_id,l.project_id,l.chat_id)=(r.tenant_key,r.app_id,r.project_id,r.target_chat_id)
		WHERE (r.tenant_key,r.app_id,r.project_id,r.target_chat_id,r.version)=($1,$2,$3,$4,$5)
		AND r.public_submission_count)`, actor.TenantKey, actor.AppID, id, target, run.version).Scan(&allowed)
	return allowed, err
}

func (s *Service) publish(ctx context.Context, actor identity.Principal, id, runID string) (chat.DeliveryStatus, error) {
	run, err := s.authorizedRun(ctx, actor, id, runID)
	if err != nil {
		return chat.DeliveryStatus{}, err
	}
	if !s.sendEnabled {
		return chat.DeliveryStatus{}, errDenied
	}
	key, content := run.receipt(id), run.content()
	ok, err := s.authorizeSend(ctx, actor, key, run.Target, content)
	if err != nil {
		return chat.DeliveryStatus{}, err
	}
	if !ok {
		return chat.DeliveryStatus{}, errDenied
	}
	return s.chat.SendBriefing(ctx, actor, run.Target, key.OperationID, content, key, s.authorizeSend)
}

func (s *Service) LookupAuthorizedReceipts(ctx context.Context, actor identity.Principal, keys []chat.BusinessReceiptKey) ([]chat.BusinessReceiptKey, error) {
	var allowed []chat.BusinessReceiptKey
	for _, key := range keys {
		if key.Target.TargetKind != "briefing" {
			continue
		}
		if err := s.require(ctx, actor, "project.read", key.Target.ProjectID); err != nil {
			if errors.Is(err, errDenied) {
				continue
			}
			return nil, err
		}
		// Receipt visibility depends on current project and original group access.
		// It never grants access to the older report snapshot.
		run, err := s.run(ctx, actor, key.Target.ProjectID, key.Target.TargetID)
		if errors.Is(err, errDenied) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if run.Target == "" || run.Delivery == "not_sent" || run.receipt(key.Target.ProjectID) != key {
			continue
		}
		if _, err := s.chat.ProjectGroup(ctx, actor, run.Target); err != nil {
			if errors.Is(err, chat.ErrAccessLost) {
				continue
			}
			return nil, err
		}
		allowed = append(allowed, key)
	}
	return allowed, nil
}
