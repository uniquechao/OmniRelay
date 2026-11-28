package group

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

func (s *Service) bindEvidencePeriod(ctx context.Context, tx pgx.Tx, w *work) error {
	p := w.Plan.EvidencePeriod
	if p.Timezone == "" {
		var received time.Time
		err := tx.QueryRow(ctx, `SELECT w.deadline-interval '10 minutes',coalesce(b.business_timezone,'Asia/Shanghai')
 FROM agent_group_work w LEFT JOIN project_task_boards b USING(tenant_key,app_id,project_id) WHERE w.id=$1 FOR UPDATE OF w`, w.Work).Scan(&received, &p.Timezone)
		if err != nil {
			return err
		}
		if err = resolveEvidenceDates(p, received); err != nil {
			return err
		}
		// Only the period is updated: confirmation and revision may have separate
		// ephemeral projections, which must never overwrite the original plan.
		if _, err = tx.Exec(ctx, `UPDATE agent_group_work SET semantic_plan=jsonb_set(semantic_plan,'{evidence_period}',$2::jsonb) WHERE id=$1`, w.Work, p); err != nil {
			return err
		}
	}
	return nil
}

func resolveEvidenceDates(p *routing.EvidencePeriod, received time.Time) error {
	if p.Kind == "range" {
		return nil
	}
	zone, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return err
	}
	local := received.In(zone)
	year, month, day := local.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, zone)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	from, to := monday, today
	if p.Kind == "last_week" {
		from, to = monday.AddDate(0, 0, -7), monday.AddDate(0, 0, -1)
	}
	p.From, p.To = from.Format(time.DateOnly), to.Format(time.DateOnly)
	return nil
}
