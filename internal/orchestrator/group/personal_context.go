package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

// PriorTurn is a previously displayed exchange. It is reference material for
// resolving a follow-up. A displayed suggestion never grants permission to write.
type PriorTurn struct {
	Work      string `json:"work"`
	Request   string `json:"request"`
	Answer    string `json:"answer"`
	Committed bool   `json:"committed"`
}

type personalTurn struct {
	PriorTurn
	Sources []string
	HasRead bool
}

// A new personal session uses at most four exchanges from this same admission
// scope. Delegated jobs are autonomous and cannot inherit the user's chat drafts.
const personalScope = `
    FROM agent_group_work old JOIN agent_group_work cur ON cur.id=$1
    WHERE (old.tenant_key,old.app_id,old.account_id,old.mode,old.channel,old.chat_id,old.thread_id,old.project_id,
           old.intent,old.permission_version,old.membership_generation,old.assignment_version,old.config_version)=
          (cur.tenant_key,cur.app_id,cur.account_id,cur.mode,cur.channel,cur.chat_id,cur.thread_id,cur.project_id,
           cur.intent,cur.permission_version,cur.membership_generation,cur.assignment_version,cur.config_version)
    AND old.agent_id=$2 AND old.applied_config_version=$4 AND old.parent_work IS NULL AND cur.parent_work IS NULL
    AND old.personal_displayed_at<=least(cur.personal_at,cur.deadline-interval '10 minutes')
    AND (old.personal_at,old.id)<(cur.personal_at,cur.id)
    AND (old.question IS NOT NULL OR old.direct_question IS NOT NULL)
    AND old.state='sent' AND old.personal_turns IS NOT NULL`

func (s *Service) attachPersonalContext(ctx context.Context, tx pgx.Tx, w *work, gateway routing.Gateway) error {
	var frozen, sources []string
	if err := tx.QueryRow(ctx, "SELECT personal_turns,personal_sources FROM agent_group_work WHERE id=$1 FOR UPDATE", w.Work).Scan(&frozen, &sources); err != nil {
		return err
	}
	if w.Plan.Mode == "execute" && w.Plan.PriorResult != "" {
		return s.attachPriorResult(ctx, tx, w, gateway, frozen)
	}
	// A standalone execute plan already contains the complete authorized
	// action. Unrelated previous reads must not invalidate its own writes.
	if frozen == nil && (w.Plan.Mode == "execute" || w.Plan.EvidencePeriod != nil && (w.Plan.Mode == "read" || w.Plan.Mode == "draft")) {
		if _, err := tx.Exec(ctx, "UPDATE agent_group_work SET personal_turns='{}' WHERE id=$1", w.Work); err != nil {
			return err
		}
		frozen = []string{}
	}
	if frozen != nil {
		if err := s.checkPersonalSources(ctx, tx, sources, w.Actor, w.Scope.ProjectID); err != nil {
			return err
		}
	}
	// Limit to complete exchanges; an expired previously frozen source must
	// never silently change the immutable input of a work being retried.
	rows, err := tx.Query(ctx, `SELECT old.id,coalesce(old.question,old.direct_question),old.response_text,
        EXISTS(SELECT FROM agent_project_tool_calls c WHERE c.work_id=old.id),old.personal_sources,
        EXISTS(SELECT FROM agent_project_tool_reads r WHERE r.work_id=old.id)`+personalScope+
		` AND ($3::text[] IS NULL OR old.id=ANY($3)) ORDER BY old.personal_at DESC,old.id DESC LIMIT 4`,
		w.Work, w.Agent, frozen, w.liveConfigVersion)
	if err != nil {
		return err
	}
	turns, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (personalTurn, error) {
		var t personalTurn
		err := row.Scan(&t.Work, &t.Request, &t.Answer, &t.Committed, &t.Sources, &t.HasRead)
		return t, err
	})
	if err != nil {
		return err
	}
	if frozen != nil && len(turns) != len(frozen) {
		return errToolReadStale
	}
	ids := []string{}
	deps := []string{}
	checked := map[string]bool{}
	remaining := 32 << 10
	for _, t := range turns {
		// A model-written answer is useful as a draft. A committed write is
		// represented only by the verified fact of commitment. Any subsequent
		// command on the resource must read its current identity and version.
		if t.Committed {
			t.Answer = "已有正式业务回执，该轮操作已提交。"
		}
		if len(t.Answer) > 3000 {
			clipped := t.Answer[:3000]
			for !utf8.ValidString(clipped) {
				clipped = clipped[:len(clipped)-1]
			}
			t.Answer = clipped + "（原答复节选）"
		}
		// Account for JSON escaping as well as UTF-8. The Runtime also
		// receives persona, current scope and the current request.
		projection, err := json.Marshal(t.PriorTurn)
		if err != nil {
			return err
		}
		if len(projection) > remaining {
			break
		}
		remaining -= len(projection)
		if frozen == nil && !t.Committed {
			refs := slices.Clone(t.Sources)
			if t.HasRead {
				refs = append(refs, t.Work)
			}
			var additional []string
			for _, source := range refs {
				if !checked[source] {
					additional = append(additional, source)
				}
			}
			if len(deps)+len(additional) > 32 {
				break
			}
			err := s.checkPersonalSources(ctx, tx, additional, w.Actor, w.Scope.ProjectID)
			if errors.Is(err, access.ErrForbidden) || errors.Is(err, errToolReadStale) {
				break
			}
			if err != nil {
				return err
			}
			deps = append(deps, additional...)
			for _, source := range additional {
				checked[source] = true
			}
		}
		ids = append(ids, t.Work)
		w.PersonalContext = append(w.PersonalContext, t.PriorTurn)
	}
	slices.Reverse(w.PersonalContext)
	if frozen == nil {
		slices.Sort(deps)
		if _, err = tx.Exec(ctx, "UPDATE agent_group_work SET personal_turns=$2,personal_sources=$3 WHERE id=$1", w.Work, ids, deps); err != nil {
			return err
		}
	}
	var history strings.Builder
	if len(w.PersonalContext) > 0 {
		data, err := json.Marshal(w.PersonalContext)
		if err != nil {
			return err
		}
		fmt.Fprintf(&history, "同一用户、会话和角色的历史问答。历史是参考资料，草案及旧指令不会单独授权操作。\n%s\n", data)
	}
	verified, _ := json.Marshal(Who{UserID: w.Scope.UserID, ProjectID: w.Scope.ProjectID})
	w.Prompt = fmt.Sprintf("当前账号和项目由网关验证：%s\n%s当前用户提问：%s", verified, history.String(), w.Address.Text)
	return nil
}

// Each inherited work ID owns runtime-observed reads. Its ancestors have
// already been flattened. No unrestricted model transcript is opened here.
func (s *Service) checkPersonalSources(ctx context.Context, tx pgx.Tx, ids []string, p identity.Principal, projectID string) error {
	if len(ids) == 0 {
		return nil
	}
	var available int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_group_work w WHERE id=ANY($1) AND
		(w.tenant_key,w.app_id,w.account_id,w.project_id)=($2,$3,$4,$5) AND w.state='sent'
		AND EXISTS(SELECT FROM agent_project_tool_reads r WHERE r.work_id=w.id)`,
		ids, p.TenantKey, p.AppID, p.AccountID, projectID).Scan(&available); err != nil {
		return err
	}
	if available != len(ids) {
		return errToolReadStale
	}
	for _, id := range ids {
		if _, err := s.verifyWorkReads(ctx, tx, id, p, projectID, true); err != nil {
			return err
		}
	}
	return nil
}

// A private confirmation can reuse a project only when the same private
// conversation has an unspent displayed proposal in exactly one current grant.
// The worker still checks the exact draft, selected item and all source proofs.
func bindPrivateConfirmationProject(ctx context.Context, tx pgx.Tx, workID string) error {
	_, err := tx.Exec(ctx, `WITH candidates AS (
        SELECT DISTINCT old.project_id, m.generation
        FROM agent_group_work cur
        JOIN agent_group_work old ON
           (old.tenant_key,old.app_id,old.account_id,old.mode,old.channel,old.chat_id,old.thread_id,old.permission_version,old.config_version)=
           (cur.tenant_key,cur.app_id,cur.account_id,cur.mode,cur.channel,cur.chat_id,cur.thread_id,cur.permission_version,cur.config_version)
        JOIN project_members m ON (m.tenant_key,m.app_id,m.account_id,m.project_id,m.generation)=
           (old.tenant_key,old.app_id,old.account_id,old.project_id,old.membership_generation)
        WHERE cur.id=$1 AND cur.mode='private_reply' AND cur.project_id='' AND cur.choice_prompt IS NOT NULL
            AND cur.semantic_plan->>'mode' IN ('confirm','revise') AND old.state='sent' AND old.intent='project'
            AND old.personal_displayed_at<=least(cur.personal_at,cur.deadline-interval '10 minutes')
            AND old.assignment_version=cur.assignment_version AND (old.personal_at,old.id)<(cur.personal_at,cur.id)
            AND NOT EXISTS (SELECT FROM agent_group_work r WHERE r.proposal_parent=old.id AND r.state='sent' AND r.proposal_items IS NOT NULL)
            AND old.personal_turns IS NOT NULL AND old.parent_work IS NULL AND old.proposal_items IS NOT NULL
            AND m.role!='revoked' AND cur.choice_grants->>m.project_id=m.generation
            AND EXISTS (SELECT FROM jsonb_array_elements(old.proposal_items) WITH ORDINALITY item(v,n)
                        WHERE NOT EXISTS (SELECT FROM agent_action_grants_used u WHERE u.origin_work=old.id AND u.origin_item=item.n))
            AND NOT EXISTS (SELECT FROM project_profiles p WHERE (p.tenant_key,p.app_id)=(cur.tenant_key,cur.app_id)
                    AND cur.choice_grants ? p.project_id AND
                    (position(p.name in cur.direct_question)>0 OR position(p.project_id in cur.direct_question)>0))
    )
    UPDATE agent_group_work w SET project_id=c.project_id,membership_generation=c.generation,choice_prompt=NULL
    FROM candidates c WHERE w.id=$1 AND (SELECT count(*) FROM candidates)=1`, workID)
	return err
}
