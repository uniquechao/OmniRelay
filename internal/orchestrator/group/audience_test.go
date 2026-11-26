package group

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestPrivateReplyGate(t *testing.T) {
	g, _, _, _ := openImageGate(t)
	send := func(account, chat, message string) (string, error) {
		var id string
		err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			var err error
			id, err = Accept(g.ctx, tx, g.Channels["internal"], Envelope{Tenant: g.Tenant, App: g.App, Channel: "internal", Account: account, Chat: chat, Message: message,
				Mode: PrivateReply, Text: "我是谁", Received: time.Now()})
			return err
		})
		return id, err
	}
	// An enabled account with no project membership can use the global query Agent.
	if _, err := send("outside", "dm-bob", "wrong-owner"); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("private peer mismatch", err)
	}
	id, err := send("outside", "dm-outside", "private-in")
	if err != nil {
		t.Fatal(err)
	}
	g.step()
	g.step()
	g.state(id, "sent")
	again, err := send("outside", "dm-outside", "private-in")
	if err != nil || again != id || len(g.replies) != 1 {
		t.Fatal("private replay", err)
	}
	if g.replies[0].Message != "private-in" || g.replies[0].Chat != "dm-outside" || g.replies[0].Mode != PrivateReply || g.texts[0] != "全局查询助手 · 全局服务空间\n你的账号是其他（outside）。" {
		t.Fatal("wrong private reply")
	}
	pm := g.mention("alice", "group-in")
	g.step()
	g.step()
	dm, err := send("alice", "dm-alice", "dm-in")
	if err != nil {
		t.Fatal(err)
	}
	g.step()
	g.step()
	var separate bool
	err = g.DB.QueryRow(g.ctx, `SELECT a.agent_id!=b.agent_id AND a.session_id!=b.session_id FROM agent_group_work a,agent_group_work b WHERE a.id=$1 AND b.id=$2`, pm, dm).Scan(&separate)
	if err != nil || !separate {
		t.Fatal("group and DM shared persona or session", err)
	}
}

func (g *gate) plan(actor string, at time.Time, targets ...NoticeTarget) (string, error) {
	openID := map[string]string{"alice": "login-a", "bob": "login-b"}[actor]
	return g.CreateNoticePlan(g.ctx, identity.Principal{TenantKey: g.Tenant, AppID: g.App, AccountID: actor, OpenID: openID},
		NoticeSpec{Project: "north", Channel: "internal", At: at, Targets: targets})
}

func TestDirectedNoticeGate(t *testing.T) {
	g, _, _, _ := openImageGate(t)
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	g.sql("UPDATE accounts SET name='PRIVATE_ONLY_GATE' WHERE id='bob'")
	at := time.Now().UTC().Add(50 * time.Second).Truncate(time.Second)
	mux := http.NewServeMux()
	operator := identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "alice", OpenID: "login-a"}
	auth := func(w http.ResponseWriter, r *http.Request, policy identity.SessionPolicy) (identity.Principal, bool) {
		if policy != identity.RefreshSessionWithCSRF {
			t.Fatal("notice control omitted CSRF policy")
		}
		return operator, true
	}
	g.RegisterNotices(mux, auth, "internal", func(context.Context) (int64, error) { return 0, nil })
	request := func(when time.Time, want int) string {
		payload := fmt.Sprintf(`{"at":%q,"targets":[{"mode":"person_notice","target":"bob"},{"mode":"group_notice","target":"group-north"}]}`, when.Format(time.RFC3339))
		req := httptest.NewRequest("POST", "/api/v1/projects/north/agent-notices", strings.NewReader(payload))
		req.Header.Set("Idempotency-Key", "request-one")
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("create status %d, want %d: %s", resp.Code, want, resp.Body.String())
		}
		var result struct{ ID string }
		if want == 202 {
			if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result.ID
	}
	id := request(at, 202)
	if id == "" || request(at, 202) != id {
		t.Fatal("registration replay failed")
	}
	request(at.Add(time.Hour), 409)
	for range 4 {
		if _, err := g.plan("alice", at, NoticeTarget{GroupNotice, "group-north"}); err != nil {
			t.Fatal(err)
		}
	}
	var native string
	for {
		var bound int
		err := g.DB.QueryRow(g.ctx, "SELECT coalesce(max(native_id) FILTER (WHERE id=$1),''),count(native_id) FROM agent_notice_plans", id).Scan(&native, &bound)
		if err != nil || time.Until(at) < 5*time.Second {
			t.Fatal("native timer was not bound before restart", err)
		}
		if bound == 5 && native != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := exec.Command("docker", "restart", "-t", "1", os.Getenv("OMNI_GATE_CONTAINER")).CombinedOutput(); err != nil {
		t.Fatalf("restart with active timer: %v %s", err, out)
	}
	// No inbound message exists for either recipient. The only producer is the native timer.
	until := time.Now().Add(60 * time.Second)
	for {
		var count int
		err := g.DB.QueryRow(g.ctx, "SELECT count(*) FROM agent_group_work WHERE plan_id IS NOT NULL").Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 6 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("Runtime did not publish the native occurrence")
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, err := g.scheduleEvent(g.ctx, "prepare", scheduleEvent{Plan: id})
	if err != nil || b.Native != native {
		t.Fatal("native timer changed on recovery", err)
	}
	event := scheduleEvent{Plan: id, Epoch: b.Epoch, At: b.At, Native: b.Native}
	// Replayed occurrence on either Go replica must yield the original two works.
	if _, err = g.scheduleEvent(g.ctx, "occur", event); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		g.step()
	}
	if len(g.replies) != 6 {
		t.Fatalf("deliveries = %d", len(g.replies))
	}
	for i, e := range g.replies {
		if e.Message != "" || e.Thread != "" || !strings.HasPrefix(g.texts[i], "项目 PM · 北辰项目") {
			t.Fatal("proactive message reused an inbound reference")
		}
		switch e.Mode {
		case GroupNotice:
			if e.Recipient != "group-north" || strings.Contains(g.texts[i], "PRIVATE_ONLY_GATE") {
				t.Fatal("private data in group")
			}
		case PersonNotice:
			if e.Recipient != "person-bob" || !strings.Contains(g.texts[i], "PRIVATE_ONLY_GATE") {
				t.Fatal("missing bound personal reminder")
			}
		default:
			t.Fatal("unexpected addressing mode", e.Mode)
		}
	}
	var sessions, receipts, sent int
	err = g.DB.QueryRow(g.ctx, `SELECT count(DISTINCT session_id),count(DISTINCT platform_message_id),count(*) FROM agent_group_work WHERE plan_id=$1 AND state='sent'`, id).Scan(&sessions, &receipts, &sent)
	if err != nil || sessions != 2 || receipts != 2 || sent != 2 {
		t.Fatal("not two distinct audience sessions and receipts", sessions, receipts, sent, err)
	}
	if _, err = g.scheduleEvent(g.ctx, "occur", event); err != nil {
		t.Fatal(err)
	}
	if err := g.Step(g.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("replay added work", err)
	}
	cancel := httptest.NewRequest("DELETE", "/api/v1/projects/north/agent-notices/"+id, nil)
	response := httptest.NewRecorder()
	operator.TenantKey = "other"
	mux.ServeHTTP(response, cancel)
	if response.Code != http.StatusForbidden {
		t.Fatal("foreign tenant could cancel plan", response.Code)
	}
	operator.TenantKey = g.Tenant
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, cancel)
	if response.Code != http.StatusNoContent || request(at, 202) != id {
		t.Fatal("cancellation or idempotent retry failed", response.Code)
	}
	if _, err := g.scheduleEvent(g.ctx, "occur", event); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("registration replay resurrected cancelled timer", err)
	}
}

func TestNoticePermissionGate(t *testing.T) {
	g := newGate(t)
	at := time.Now().UTC().Add(60 * time.Second).Truncate(time.Second)
	both := []NoticeTarget{{GroupNotice, "group-north"}, {PersonNotice, "bob"}}
	if _, err := g.plan("alice", at, both...); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("ordinary member registered a send", err)
	}
	g.sql("UPDATE project_members SET role='manager' WHERE account_id='alice'")
	if _, err := g.plan("alice", at, NoticeTarget{PersonNotice, "outside"}); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("nonmember target", err)
	}
	id, err := g.plan("alice", at, both...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.scheduleEvent(g.ctx, "prepare", scheduleEvent{Plan: id})
	if err != nil {
		t.Fatal(err)
	}
	e := scheduleEvent{Plan: id, At: at, Epoch: b.Epoch, Native: "native-fixture"}
	if _, err = g.scheduleEvent(g.ctx, "bind", e); err != nil {
		t.Fatal(err)
	}
	if _, err = g.scheduleEvent(g.ctx, "occur", e); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("early fire", err)
	}
	for _, change := range []func(*scheduleEvent){func(x *scheduleEvent) { x.Epoch++ }, func(x *scheduleEvent) { x.At = x.At.Add(time.Hour) }, func(x *scheduleEvent) { x.Native = "other" }} {
		wrong := e
		change(&wrong)
		if _, err := g.scheduleEvent(g.ctx, "bind", wrong); !errors.Is(err, access.ErrForbidden) {
			t.Fatal("stale or unbound schedule", err)
		}
	}
	// Advance only the fixture clock for permission fault injection. The image gate exercises native time.
	e.At = time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	g.sql("UPDATE agent_notice_plans SET due_at=$2 WHERE id=$1", id, e.At)
	if _, err = g.scheduleEvent(g.ctx, "occur", e); err != nil {
		t.Fatal(err)
	}
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) {
		c := ToolCall{Binding: b, Turn: 1}
		if _, err := g.useTurn(ctx, c, true); err != nil {
			return dsh.Result{}, err
		}
		who, err := g.useTurn(ctx, c, false)
		if err != nil {
			return dsh.Result{}, err
		}
		if b.Kind == string(GroupNotice) {
			body, _ := json.Marshal(who)
			if strings.Contains(string(body), "user_") {
				t.Fatal("personal fields in group tool receipt")
			}
		}
		return dsh.Result{Session: b.Session, Turn: 1}, nil
	}
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='bob'")
	for i := 0; i < 3; i++ {
		g.step()
	}
	if len(g.replies) != 1 || g.replies[0].Mode != GroupNotice {
		t.Fatal("revoked recipient received or blocked unrelated group")
	}
	g.sql("UPDATE agent_notice_plans SET enabled=false WHERE id=$1", id)
	if _, err := g.scheduleEvent(g.ctx, "occur", e); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("disabled plan resurrected", err)
	}
	// A personal message with a completed tool receipt also needs a current grant at delivery.
	g.sql("UPDATE project_members SET role='reader' WHERE account_id='bob'")
	id, err = g.plan("alice", at, NoticeTarget{PersonNotice, "bob"})
	if err != nil {
		t.Fatal(err)
	}
	e.Plan, e.At, e.Native = id, at, "native-second"
	if _, err = g.scheduleEvent(g.ctx, "bind", e); err != nil {
		t.Fatal(err)
	}
	e.At = time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	g.sql("UPDATE agent_notice_plans SET due_at=$2 WHERE id=$1", id, e.At)
	if _, err = g.scheduleEvent(g.ctx, "occur", e); err != nil {
		t.Fatal(err)
	}
	g.step()
	var workID string
	if err = g.DB.QueryRow(g.ctx, "SELECT id FROM agent_group_work WHERE plan_id=$1 AND state='ready' AND tool_receipt IS NOT NULL", id).Scan(&workID); err != nil {
		t.Fatal("missing completed private tool receipt", err)
	}
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='bob'")
	g.step()
	g.state(workID, "suppressed")
	if len(g.replies) != 1 {
		t.Fatal("revoked member received a prepared personal notice")
	}
}
