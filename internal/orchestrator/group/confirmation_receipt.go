package group

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/agentadmin"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/project"
)

// Single actions based on prior content can change the source that informed
// them. Commit their authoritative receipt with the action, before that change
// could invalidate publication or cause another execution after recovery.
func (w work) settlesPersonalWrite() bool {
	return w.Plan.Mode == "confirm" && w.OriginWork != "" ||
		w.Plan.Mode == "execute" && w.Plan.PriorResult != "" && len(w.Plan.Actions) == 1
}

func (s *Service) finishPersonalConfirmation(ctx context.Context, tx pgx.Tx, w work, c projectCall) error {
	tag, err := tx.Exec(ctx, `UPDATE agent_project_tool_calls c SET confirmation_args=$4
      FROM agent_action_grants_used g WHERE (g.work_id,g.slot)=(c.work_id,c.slot) AND
      c.work_id=$1 AND c.slot=$2 AND c.tool=$3 AND g.item=1
      AND g.origin_work IS NOT DISTINCT FROM nullif($5,'') AND g.origin_item IS NOT DISTINCT FROM nullif($6,0)`,
		w.Work, c.Slot, c.Name, c.Args, w.OriginWork, w.OriginItem)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errSlot
	}
	// Failed preceding slots are also independently recorded by the Gateway.
	rows, err := tx.Query(ctx, `SELECT f.slot,f.tool,f.status FROM agent_project_tool_failures f WHERE f.work_id=$1
       AND NOT EXISTS(SELECT FROM agent_project_tool_calls c WHERE (c.work_id,c.slot)=(f.work_id,f.slot)) ORDER BY f.slot`, w.Work)
	if err != nil {
		return err
	}
	failed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dsh.FailedWrite])
	if err != nil {
		return err
	}
	answer, err := s.verifiedAnswer(ctx, tx, w, dsh.Result{Kind: "writes", WriteSlots: []int{c.Slot}, FailedWrites: failed})
	if err != nil {
		return err
	}
	if err = agentadmin.Applied(ctx, tx, s.Tenant, s.App, w.configScope(), w.Role, w.ConfigVersion); err != nil {
		return err
	}
	state := "sent"
	if w.Address.Mode == PrivateReply {
		state = "ready"
	}
	_, err = tx.Exec(ctx, `UPDATE agent_group_work SET state=$2,response_text=$3,applied_config_version=$4,
       personal_turns=CASE WHEN $5 THEN personal_turns ELSE '{}'::text[] END,personal_sources='{}',
       personal_displayed_at=CASE WHEN $2='sent' THEN clock_timestamp() END,lease_until=NULL WHERE id=$1`,
		w.Work, state, w.signature()+"\n"+answer, w.ConfigVersion, w.Plan.PriorResult != "")
	return err
}

// A confirmed receipt contains domain identifiers, never a draft, an earlier
// source excerpt or a model answer. Check the actual committed target anew.
func (s *Service) authorizeConfirmedReceipt(ctx context.Context, tx pgx.Tx, id string, p identity.Principal, projectID string) (bool, error) {
	var name string
	var args pmtools.Args
	err := tx.QueryRow(ctx, "SELECT tool,confirmation_args FROM agent_project_tool_calls WHERE work_id=$1 AND confirmation_args IS NOT NULL", id).Scan(&name, &args)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tool, known := pmtools.Lookup(name)
	if !known || s.Projects == nil {
		return true, access.ErrForbidden
	}
	ok, err := s.Roles.CheckInOrganizationMutation(ctx, tx, p, tool.Action, projectID)
	if err != nil {
		return true, err
	}
	if !ok {
		return true, access.ErrForbidden
	}
	err = s.Projects.AuthorizeToolReceipt(ctx, tx, p, projectID, name, args)
	if project.ToolStatus(err) == 403 {
		err = access.ErrForbidden
	}
	return true, err
}

// The native worker may lose the successful reply after the domain transaction
// and the delivery receipt commit. Only the exact original bound slot can be
// read back in its remaining lifetime.
func (s *Service) replayPersonalConfirmation(ctx context.Context, tx pgx.Tx, c projectCall) (json.RawMessage, error) {
	if c.Slot < 1 || c.Owner != s.Owner {
		return nil, access.ErrForbidden
	}
	var name, fp string
	var receipt json.RawMessage
	err := tx.QueryRow(ctx, `SELECT command.tool,command.fingerprint,command.receipt
      FROM agent_project_tool_calls command JOIN agent_group_work w ON w.id=command.work_id JOIN agent_instances a ON a.agent_id=w.agent_id
      WHERE command.work_id=$1 AND command.slot=$2 AND command.confirmation_args IS NOT NULL
      AND w.state IN ('ready','sending','sent','unknown','failed') AND w.agent_id=$3 AND w.session_id=$4
      AND a.owner_id=$5 AND a.owner_epoch=$6 AND w.bound_generation=$7 AND w.bound_turn=$8
      AND w.deadline>clock_timestamp() AND (w.tenant_key,w.app_id)=($9,$10)`, c.Work, c.Slot, c.Agent, c.Session, s.Owner, c.Epoch, c.Generation, c.Turn, s.Tenant, s.App).Scan(&name, &fp, &receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, access.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	w, err := s.authorize(ctx, tx, c.Work)
	if err != nil {
		return nil, err
	}
	if w.Role != c.Role || w.Kind != c.Kind || w.ConfigVersion != c.ConfigVersion {
		return nil, access.ErrForbidden
	}
	_, err = s.authorizeConfirmedReceipt(ctx, tx, c.Work, w.Actor, w.Scope.ProjectID)
	if err != nil {
		return nil, err
	}
	hash, err := toolDigest(c.Args)
	if err != nil {
		return nil, err
	}
	if name != c.Name || fp != hash {
		return nil, errSlot
	}
	return confirmationAcknowledgement(receipt)
}

func confirmationAcknowledgement(receipt json.RawMessage) (json.RawMessage, error) {
	// A domain result can include user-authored draft material. Replaying the
	// acknowledgement only requires its identity and revision, never its body.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(receipt, &fields); err != nil {
		return nil, err
	}
	ack := map[string]json.RawMessage{}
	for _, k := range []string{"business_date", "version", "task_code", "task_id", "id", "project_id", "chat_id"} {
		if v, ok := fields[k]; ok {
			ack[k] = v
		}
	}
	return json.Marshal(ack)
}
