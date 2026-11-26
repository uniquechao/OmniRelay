package group

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestTopicRevisionSuppressesStaleHistory(t *testing.T) {
	g := newGate(t)
	id := g.mention("alice", "incoming")
	g.sql("UPDATE agent_group_work SET channel='feishu',thread_id='topic',group_question='进度',group_asked_at=now() WHERE id=$1", id)
	actor := identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: "alice"}
	source := json.RawMessage(`[{"excerpt":"更正：周五"}]`)
	g.ReadThread = func(_ context.Context, _ pgx.Tx, p identity.Principal, project, chat, thread, msg string, _ time.Time) (json.RawMessage, error) {
		if p.AccountID != "alice" || project != "north" || chat != "group-north" || thread != "topic" || msg != "incoming" {
			t.Fatal("incorrect source scope")
		}
		return source, nil
	}
	read := func(attach bool) error {
		return pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error { _, err := g.threadSource(g.ctx, tx, id, actor, "north", attach); return err })
	}
	if err := read(true); err != nil {
		t.Fatal(err)
	}
	if err := read(false); err != nil {
		t.Fatal(err)
	}
	source = json.RawMessage(`[]`)
	if err := read(false); !errors.Is(err, errToolReadStale) {
		t.Fatalf("recalled source accepted: %v", err)
	}
}

func TestNoticeDiscoveryIncludesAllFivePlans(t *testing.T) {
	g := newGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	at := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	var plans []string
	for range 5 {
		id, err := g.plan("alice", at, NoticeTarget{GroupNotice, "group-north"})
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, id)
	}
	read := func() []string {
		req := httptest.NewRequest("GET", "/internal/schedules", nil)
		req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{DNSNames: []string{g.Owner}}}}}
		out := httptest.NewRecorder()
		g.Tools().ServeHTTP(out, req)
		var found []string
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &found) != nil {
			t.Fatalf("discovery: %d %s", out.Code, out.Body)
		}
		return found
	}
	slices.Sort(plans)
	if !slices.Equal(read(), plans) {
		t.Fatalf("omitted plan; want %v", plans)
	}
	g.sql("UPDATE agent_notice_plans SET enabled=false WHERE id=$1", plans[0])
	if !slices.Equal(read(), plans[1:]) {
		t.Fatal("cancelled plan rediscovered")
	}
}
