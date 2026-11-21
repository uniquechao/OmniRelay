package group

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
)

type ToolCall struct {
	dsh.Binding
	Turn int `json:"turn"`
}

func (s *Service) useTurn(ctx context.Context, c ToolCall, bind bool) (Who, error) {
	var who Who
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT w.id FROM agent_group_work w JOIN agent_instances a USING(agent_id)
             WHERE w.id=$1 AND a.agent_id=$2 AND w.session_id=$3 AND a.owner_id=$4 AND a.owner_epoch=$5
             AND w.generation=$6 AND w.state='running' AND w.deadline>now() AND w.lease_until>now()
             AND a.tenant_key=$7 AND a.app_id=$8 FOR UPDATE OF w FOR SHARE OF a`,
			c.Work, c.Agent, c.Session, s.Owner, c.Epoch, c.Generation, s.Tenant, s.App).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrForbidden
		}
		if err != nil {
			return err
		}
		w, err := s.authorize(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.Owner != s.Owner || c.Session != w.Session || c.Agent != w.Agent || c.Turn <= 0 {
			return access.ErrForbidden
		}
		if bind {
			tag, err := tx.Exec(ctx, `UPDATE agent_group_work SET bound_generation=$2,bound_turn=$3 WHERE id=$1
				AND (bound_generation IS DISTINCT FROM $2 OR bound_turn=$3)`, id, c.Generation, c.Turn)
			if err == nil && tag.RowsAffected() == 0 {
				return access.ErrForbidden
			}
			return err
		}
		var bound bool
		if err = tx.QueryRow(ctx, "SELECT coalesce(bound_generation=$2 AND bound_turn=$3,false) FROM agent_group_work WHERE id=$1", id, c.Generation, c.Turn).Scan(&bound); err != nil {
			return err
		}
		if !bound {
			return access.ErrForbidden
		}
		who = w.Scope
		_, err = tx.Exec(ctx, "UPDATE agent_group_work SET tool_turn=$2,tool_receipt=$3 WHERE id=$1", id, c.Turn, who)
		return err
	})
	return who, err
}
func (s *Service) Tools() http.Handler {
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || r.TLS.VerifiedChains[0][0].VerifyHostname(s.Owner) != nil {
			http.Error(w, "forbidden", 403)
			return
		}
		var c ToolCall
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid binding", 400)
			return
		}
		bind := r.URL.Path == "/internal/turns/start"
		who, err := s.useTurn(r.Context(), c, bind)
		if errors.Is(err, access.ErrForbidden) {
			http.Error(w, "forbidden", 403)
			return
		}
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		if bind {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(who)
	}
	mux.HandleFunc("POST /internal/tools/context.whoami", handle)
	mux.HandleFunc("POST /internal/turns/start", handle)
	return mux
}
