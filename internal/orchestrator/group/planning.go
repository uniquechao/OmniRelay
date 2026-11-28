package group

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/project"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func (s *Service) claim(ctx context.Context) (work, error) {
	for {
		w, err := s.claimOne(ctx)
		if err != nil || w.State != "planning" {
			return w, err
		}
		if err = s.planWork(ctx, w); err != nil {
			return work{}, err
		}
	}
}
func (s *Service) planWork(ctx context.Context, w work) error {
	var p routing.Plan
	var err error
	if s.PlanIntent == nil {
		err = errors.New("semantic planner unavailable")
	} else {
		p, err = s.PlanIntent(ctx, w.PlanRequest)
	}
	if err == nil {
		err = p.Validate(w.PlanRequest)
	}
	if err != nil {
		_, dbErr := s.DB.Exec(ctx, "UPDATE agent_group_work SET lease_until=NULL,available_at=now()+interval '3 seconds' WHERE id=$1 AND generation=$2 AND needs_plan", w.Work, w.Generation)
		return errors.Join(err, dbErr)
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if p.Project != "" && p.Intent != "admin" {
			if err := s.bindRequestedProject(ctx, tx, w, p.Project); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_group_work SET semantic_plan=$3,needs_plan=false,intent=$4,
 write_requested=$5,lease_until=NULL,
 project_id=CASE WHEN mode='private_reply' AND $4='admin' THEN '' ELSE project_id END,
 membership_generation=CASE WHEN mode='private_reply' AND $4='admin' THEN '' ELSE membership_generation END,
 choice_prompt=CASE WHEN mode='private_reply' AND $4='admin' THEN NULL ELSE choice_prompt END
 WHERE id=$1 AND generation=$2 AND state='route' AND needs_plan`, w.Work, w.Generation, p.JSON(), p.Intent, p.Mode == "execute" || p.Mode == "confirm")
		if err == nil && tag.RowsAffected() == 1 && p.Project == "" && (p.Mode == "confirm" || p.Mode == "revise") {
			return bindPrivateConfirmationProject(ctx, tx, w.Work)
		}
		return err
	})
}

func (s *Service) bindRequestedProject(ctx context.Context, tx pgx.Tx, w work, selector string) error {
	var actor, projectID string
	var mode Mode
	var admitted map[string]string
	err := tx.QueryRow(ctx, "SELECT account_id,project_id,mode,coalesce(choice_grants,'{}') FROM agent_group_work WHERE id=$1 AND generation=$2 AND needs_plan FOR UPDATE", w.Work, w.Generation).Scan(&actor, &projectID, &mode, &admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ps, err := project.VisiblePortfolio(ctx, tx, s.Tenant, s.App, actor)
	if err != nil {
		return err
	}
	var matches []string
	for _, p := range ps {
		if p.Name == selector || p.ID == selector {
			matches = append(matches, p.ID)
		}
	}
	if mode != PrivateReply {
		if len(matches) == 1 && matches[0] == projectID {
			return nil
		}
		_, err = tx.Exec(ctx, "UPDATE agent_group_work SET state='suppressed',reason='requested_project_outside_scope',lease_until=NULL WHERE id=$1", w.Work)
		return err
	}
	if len(matches) == 1 && admitted[matches[0]] != "" {
		tag, err := tx.Exec(ctx, `UPDATE agent_group_work w SET project_id=m.project_id,membership_generation=m.generation,choice_prompt=NULL FROM project_members m
 WHERE w.id=$1 AND (m.tenant_key,m.app_id,m.account_id)=(w.tenant_key,w.app_id,w.account_id) AND m.project_id=$2 AND m.role!='revoked' AND m.generation=$3`, w.Work, matches[0], admitted[matches[0]])
		if err != nil || tag.RowsAffected() == 1 {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE agent_group_work SET project_id='',membership_generation='',choice_prompt=$2 WHERE id=$1", w.Work, projectChoice(ps))
	return err
}

// Validate the original authenticated audience before sending even the user's
// own message to the isolated planner. Role admission occurs after classification.
func (s *Service) planningAccess(ctx context.Context, tx pgx.Tx, id string) error {
	var e Envelope
	var version int64
	err := tx.QueryRow(ctx, `SELECT w.tenant_key,w.app_id,w.channel,w.account_id,w.chat_id,w.thread_id,w.message_id,w.mode,w.project_id,w.config_version,w.assignment_version
 FROM agent_group_work w JOIN accounts a ON (a.tenant_key,a.app_id,a.id,a.permission_version)=(w.tenant_key,w.app_id,w.account_id,w.permission_version)
 WHERE w.id=$1 AND a.active AND (w.project_id='' OR EXISTS(SELECT FROM project_members m WHERE
 (m.tenant_key,m.app_id,m.project_id,m.account_id,m.generation)=(w.tenant_key,w.app_id,w.project_id,w.account_id,w.membership_generation) AND m.role!='revoked'))`, id).Scan(&e.Tenant, &e.App, &e.Channel, &e.Account, &e.Chat, &e.Thread, &e.Message, &e.Mode, &e.Project, &e.Config, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrForbidden
	}
	if err != nil {
		return err
	}
	ch, ok := s.Channels[e.Channel]
	if !ok {
		return access.ErrForbidden
	}
	a, err := ch.Resolve(ctx, tx, e)
	if err != nil {
		return err
	}
	if a.Project != e.Project || a.Version != version {
		return access.ErrForbidden
	}
	return nil
}

// A presentation hint, with no prior text or IDs, helps interpret a follow-up.
// Actual binding still checks all prior-source permissions, the selected item,
// the target project, its current version and one-time consumption.
const displayedProposalQuery = `SELECT EXISTS(
 SELECT FROM (
   SELECT p.id,p.proposal_items FROM agent_group_work p JOIN agent_group_work cur ON cur.id=$1
   WHERE (p.tenant_key,p.app_id,p.account_id,p.mode,p.channel,p.chat_id,p.thread_id,p.permission_version,p.assignment_version,p.config_version)=
   (cur.tenant_key,cur.app_id,cur.account_id,cur.mode,cur.channel,cur.chat_id,cur.thread_id,cur.permission_version,cur.assignment_version,cur.config_version)
   AND cur.mode IN ('project_chat','private_reply') AND p.parent_work IS NULL AND cur.parent_work IS NULL
   AND cur.requested_intent IN ('','project') AND p.intent='project' AND p.state='sent' AND p.personal_turns IS NOT NULL
   AND ((p.project_id,p.membership_generation)=(cur.project_id,cur.membership_generation) OR
       cur.mode='private_reply' AND cur.project_id='' AND cur.choice_grants->>p.project_id=p.membership_generation)
   AND p.personal_displayed_at<=least(cur.personal_at,cur.deadline-interval '10 minutes')
   AND (p.personal_at,p.id)<(cur.personal_at,cur.id) ORDER BY p.personal_at DESC,p.id DESC LIMIT 4
 ) old CROSS JOIN LATERAL jsonb_array_elements(old.proposal_items) WITH ORDINALITY item(value,pos)
 WHERE NOT EXISTS (SELECT FROM agent_action_grants_used u WHERE u.origin_work=old.id AND u.origin_item=item.pos)
 AND NOT EXISTS (SELECT FROM agent_group_work revision WHERE revision.proposal_parent=old.id AND revision.state='sent' AND revision.proposal_items IS NOT NULL)
)`
