package drive

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"golang.org/x/sync/errgroup"
)

type syncProgress struct {
	Groups           int        `json:"groups"`
	HistoryPending   int        `json:"history_pending"`
	HistoryAttention int        `json:"history_attention"`
	Unindexed        int64      `json:"unindexed_messages"`
	UnresolvedWiki   int64      `json:"unresolved_wiki"`
	Pending          int64      `json:"pending_sources"`
	Complete         int64      `json:"complete_sources"`
	Attention        int64      `json:"attention_sources"`
	LastCompleted    *time.Time `json:"last_completed_at,omitempty"`
}

type groupProgress struct {
	Chat, Project                    string
	HistoryPending, HistoryAttention bool
	Unindexed, UnresolvedWiki        int64
	Pending, Complete, Attention     int64
	LastCompleted                    *time.Time
}

func (s *Service) getSyncProgress(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	result, err := s.syncProgress(ctx, p)
	if err != nil {
		problem(w, 503, "source_unavailable")
		return
	}
	jsonResponse(w, 200, result)
}

func (s *Service) syncProgress(ctx context.Context, p identity.Principal) (syncProgress, error) {
	var result syncProgress
	rows, err := s.pool.Query(ctx, `SELECT c.chat_id,coalesce(l.project_id,''),
		coalesce(h.pending,true),coalesce(h.attention,false),m.unindexed,m.unresolved_wiki,
		f.pending,f.complete,f.attention,f.last_completed
	FROM feishu_chat_conversations c JOIN feishu_bot_configs b USING (tenant_key,app_id)
	JOIN accounts a ON (a.tenant_key,a.app_id,a.id)=(c.tenant_key,c.app_id,$4)
	LEFT JOIN project_chat_links l ON (l.tenant_key,l.app_id,l.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
	LEFT JOIN project_members pm ON (pm.tenant_key,pm.app_id,pm.project_id,pm.account_id)=(l.tenant_key,l.app_id,l.project_id,$4)
	    AND pm.role<>'revoked'
	CROSS JOIN LATERAL (
		SELECT bool_or(state IN ('pending','syncing')) AS pending,
		       bool_or(state IN ('partial','failed','access_lost')) AS attention
		FROM feishu_chat_sync h WHERE (h.tenant_key,h.app_id,h.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
	) h
	CROSS JOIN LATERAL (
		SELECT count(*) FILTER (WHERE NOT m.files_indexed) AS unindexed,
		       count(*) FILTER (WHERE m.files_indexed AND EXISTS (
		           SELECT FROM feishu_message_wiki_links n JOIN feishu_wiki_targets t USING(tenant_key,app_id,token)
		           WHERE (n.tenant_key,n.app_id,n.message_id)=(m.tenant_key,m.app_id,m.message_id)
		           AND coalesce(t.kind,'')='')) AS unresolved_wiki
		FROM feishu_chat_messages m WHERE (m.tenant_key,m.app_id,m.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
		  AND NOT m.deleted AND NOT EXISTS (SELECT FROM feishu_candidate_event_owners o
		    WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL)
	) m
	CROSS JOIN LATERAL (
		SELECT count(*) FILTER (WHERE x.state='pending') AS pending,
		       count(*) FILTER (WHERE x.state='complete' AND x.version_id IS NOT NULL) AS complete,
		       count(*) FILTER (WHERE x.state IN ('partial','failed','expired','over_limit')) AS attention,
		       max(x.synced_at) FILTER (WHERE x.state='complete') AS last_completed
		FROM feishu_file_sources x WHERE (x.tenant_key,x.app_id,x.chat_id)=(c.tenant_key,c.app_id,c.chat_id)
	) f
	WHERE c.tenant_key=$1 AND c.app_id=$2 AND b.credentials_generation=$3 AND b.enabled
	  AND a.open_id=$5 AND a.active AND c.kind='group' AND (l.project_id IS NULL OR pm.account_id IS NOT NULL)`,
		s.tenant, s.app, s.generation, p.AccountID, p.OpenID)
	if err != nil {
		return result, err
	}
	groups, err := pgx.CollectRows(rows, pgx.RowToStructByPos[groupProgress])
	if err != nil {
		return result, err
	}
	checks, ctx := errgroup.WithContext(ctx)
	checks.SetLimit(6)
	allowed := make([]bool, len(groups))
	for i, group := range groups {
		checks.Go(func() error {
			var err error
			allowed[i], err = s.canReadGroup(ctx, p, group.Project, group.Chat)
			return err
		})
	}
	if err := checks.Wait(); err != nil {
		return result, err
	}
	for i, group := range groups {
		if !allowed[i] {
			continue
		}
		result.Groups++
		if group.HistoryPending {
			result.HistoryPending++
		}
		if group.HistoryAttention {
			result.HistoryAttention++
		}
		result.Unindexed += group.Unindexed
		result.UnresolvedWiki += group.UnresolvedWiki
		result.Pending += group.Pending
		result.Complete += group.Complete
		result.Attention += group.Attention
		if group.LastCompleted != nil && (result.LastCompleted == nil || group.LastCompleted.After(*result.LastCompleted)) {
			result.LastCompleted = group.LastCompleted
		}
	}
	return result, nil
}
