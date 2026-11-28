package group

import (
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
)

func TestClaimClipsNativeTurnToOriginalWorkDeadline(t *testing.T) {
	g := newGate(t)
	id := g.mention("alice", "budget")
	check := func(w work, shorter bool) {
		t.Helper()
		var lease, deadline time.Time
		if err := g.DB.QueryRow(g.ctx, "SELECT lease_until, deadline FROM agent_group_work WHERE id=$1", id).Scan(&lease, &deadline); err != nil {
			t.Fatal(err)
		}
		if w.ExecuteBeforeMS != lease.Add(-dsh.ReceiptReserve).UnixMilli() || lease.After(deadline) {
			t.Fatalf("incorrect reserve or lease %d %v %v", w.ExecuteBeforeMS, lease, deadline)
		}
		if shorter != lease.Equal(deadline) {
			t.Fatal("lease not clipped to admission", lease, deadline)
		}
		if !shorter && time.Until(lease) < 3*time.Minute {
			t.Fatal("normal model turn would still be cut short", lease)
		}
	}
	first, err := g.claim(g.ctx)
	if err != nil || first.Work != id {
		t.Fatal(first.Work, err)
	}
	check(first, false)
	g.sql("UPDATE agent_group_work SET lease_until=NULL,deadline=now()+interval '45 seconds' WHERE id=$1", id)
	next, err := g.claim(g.ctx)
	if err != nil || next.Work != id || next.Session != first.Session || next.Generation <= first.Generation {
		t.Fatal(next.Work, err)
	}
	check(next, true)
}
