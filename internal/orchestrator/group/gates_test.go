package group

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/dsh"
	"github.com/uniquechao/OmniRelay/migrations"
)

type gate struct {
	*Service
	t       *testing.T
	ctx     context.Context
	version int64
	replies []Envelope
	texts   []string
}

func newGate(t *testing.T) *gate {
	t.Helper()
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("agent_gate_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); base.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	roles := access.New(db, "tenant", "app")
	roles.ProjectAccess = access.LocalProjects{Pool: db}
	g := &gate{Service: &Service{DB: db, Roles: roles, Tenant: "tenant", App: "app", Owner: "localhost"}, t: t, ctx: ctx, version: 1}
	g.Channels = map[string]Channel{"internal": {Resolve: func(_ context.Context, _ pgx.Tx, e Envelope) (Assignment, error) {
		if e.Chat != "group-north" {
			return Assignment{}, access.ErrForbidden
		}
		return Assignment{Project: "north", Bot: "internal-bot", Version: g.version}, nil
	}, Reply: func(ctx context.Context, e Envelope, bot, text, id string) Receipt {
		if bot != "internal-bot" || id == "" {
			t.Fatal("unbound delivery")
		}
		g.replies = append(g.replies, e)
		g.texts = append(g.texts, text)
		return Receipt{State: "success", MessageID: "internal-" + id}
	}}}
	g.sql(`INSERT INTO accounts(id,tenant_key,app_id,open_id,name) VALUES('alice','tenant','app','login-a','小林'),('bob','tenant','app','login-b','小李'),('outside','tenant','app','login-o','其他');
    INSERT INTO project_profiles(tenant_key,app_id,project_id,name,status,version) VALUES('tenant','app','north','北辰项目','active',1);
    INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role) VALUES('tenant','app','north','alice','a','member'),('tenant','app','north','bob','b','reader')`)
	return g
}
func (g *gate) sql(q string, args ...any) {
	g.t.Helper()
	if _, err := g.DB.Exec(g.ctx, q, args...); err != nil {
		g.t.Fatal(err)
	}
}
func (g *gate) accept(user, msg string) (string, error) {
	var id string
	err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
		var err error
		id, err = Accept(g.ctx, tx, g.Channels["internal"], Envelope{Tenant: "tenant", App: "app", Channel: "internal", Account: user, Chat: "group-north", Thread: "topic-7", Message: msg, Text: "我是谁", Mention: true, Received: time.Now()})
		return err
	})
	return id, err
}
func (g *gate) mention(user, msg string) string {
	g.t.Helper()
	id, err := g.accept(user, msg)
	if err != nil {
		g.t.Fatal(err)
	}
	return id
}
func (g *gate) step() {
	g.t.Helper()
	if err := g.Step(g.ctx); err != nil {
		g.t.Fatal(err)
	}
}
func (g *gate) state(id, want string) {
	g.t.Helper()
	var got string
	if err := g.DB.QueryRow(g.ctx, "SELECT state FROM agent_group_work WHERE id=$1", id).Scan(&got); err != nil || got != want {
		g.t.Fatalf("state %q, want %q (%v)", got, want, err)
	}
}
func TestGatewayPermissionGate(t *testing.T) {
	g := newGate(t)
	if _, err := g.accept("outside", "x"); !errors.Is(err, access.ErrForbidden) {
		t.Fatal(err)
	}
	a := g.mention("alice", "one")
	if g.mention("alice", "one") != a {
		t.Fatal("replay created another work")
	}
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.claim(g.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second worker obtained a live claim: %v", err)
	}
	c := ToolCall{Binding: w.Binding, Turn: 1}
	if _, err = g.useTurn(g.ctx, c, false); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("read before turn", err)
	}
	if _, err = g.useTurn(g.ctx, c, true); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*ToolCall){func(x *ToolCall) { x.Owner = "other" }, func(x *ToolCall) { x.Agent = "other" }, func(x *ToolCall) { x.Session = "other" }, func(x *ToolCall) { x.Epoch++ }, func(x *ToolCall) { x.Generation++ }, func(x *ToolCall) { x.Turn++ }} {
		other := c
		modify(&other)
		if _, err = g.useTurn(g.ctx, other, false); !errors.Is(err, access.ErrForbidden) {
			t.Fatal("unbound tool read", err)
		}
	}
	who, err := g.useTurn(g.ctx, c, false)
	if err != nil || who.UserID != "alice" || who.ProjectName != "北辰项目" {
		t.Fatal(who, err)
	}
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='alice'")
	if _, err = g.useTurn(g.ctx, c, false); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("revoked turn read", err)
	}
	g.sql("UPDATE project_members SET role='member',generation='rejoined' WHERE account_id='alice'")
	if _, err = g.useTurn(g.ctx, c, false); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("rejoined turn revived", err)
	}
	g.sql("UPDATE agent_group_work SET lease_until=now() WHERE id=$1", a)
	g.step()
	g.state(a, "suppressed")
	b := g.mention("bob", "two")
	g.version++
	g.step()
	g.state(b, "suppressed")
	cID := g.mention("bob", "three")
	g.sql("UPDATE accounts SET active=false WHERE id='bob'")
	g.step()
	g.state(cID, "suppressed")
	if len(g.replies) != 0 {
		t.Fatal("received an unauthorized reply")
	}
}
func TestRuntimeImageGate(t *testing.T) {
	dir := os.Getenv("OMNI_GATE_CERTS")
	if dir == "" {
		t.Skip("run tests/integration/group-agent-gate.sh")
	}
	g := newGate(t)
	certs, err := dsh.TLS(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "gateway.crt"), filepath.Join(dir, "gateway.key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("OMNI_GATE_GO_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: g.Tools()}
	go server.Serve(tls.NewListener(l, certs))
	t.Cleanup(func() { _ = server.Close() })
	client, err := dsh.NewClient("https://localhost:"+os.Getenv("OMNI_GATE_RUNTIME_PORT"), certs)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise transport identity and input boundaries with valid private CA certificates.
	intruder, err := dsh.TLS(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "intruder.crt"), filepath.Join(dir, "intruder.key"))
	if err != nil {
		t.Fatal(err)
	}
	post := func(c *tls.Config, endpoint string, value any, want int) {
		t.Helper()
		body, _ := json.Marshal(value)
		tr := &http.Transport{TLSClientConfig: c}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Post(endpoint, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("expected %d, got %d", want, resp.StatusCode)
		}
	}
	privateGo := "https://127.0.0.1:" + os.Getenv("OMNI_GATE_GO_PORT") + "/internal/tools/context.whoami"
	post(intruder, privateGo, map[string]string{}, 403)
	workerTLS, err := dsh.TLS(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "localhost.crt"), filepath.Join(dir, "localhost.key"))
	if err != nil {
		t.Fatal(err)
	}
	post(workerTLS, privateGo, map[string]string{"user_id": "bob"}, 400)
	post(workerTLS, privateGo, ToolCall{}, 403)
	var last dsh.Binding
	g.Execute = func(ctx context.Context, b dsh.Binding) (dsh.Result, error) { last = b; return client.Execute(ctx, b) }
	id := g.mention("alice", "first")
	g.step()
	g.state(id, "ready")
	runtimeURL := "https://localhost:" + os.Getenv("OMNI_GATE_RUNTIME_PORT") + "/works/" + id
	wrong := last
	wrong.Owner = "another-runtime"
	post(certs, runtimeURL, wrong, 403)
	wrong = last
	wrong.Session = strings.Repeat("b", 64)
	post(certs, runtimeURL, wrong, 409)
	// Interrupt between durable DSH completion and Go acknowledgment; recreate the
	// runtime process on the original volume and reacquire the work on another Go claim.
	if out, err := exec.Command("docker", "restart", "-t", "1", os.Getenv("OMNI_GATE_CONTAINER")).CombinedOutput(); err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
	g.sql("UPDATE agent_group_work SET state='running',lease_until=now() WHERE id=$1", id)
	var done bool
	for i := 0; i < 30; i++ {
		if _, err := client.Execute(g.ctx, last); err == nil {
			done = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !done {
		t.Fatal("runtime did not recover its completed work")
	}
	g.step()
	g.step()
	g.state(id, "sent")
	if g.mention("alice", "first") != id || len(g.replies) != 1 || g.replies[0].Thread != "topic-7" || g.replies[0].Message != "first" || !strings.HasPrefix(g.texts[0], "项目 PM · 北辰项目\n小林（alice）") {
		t.Fatal("wrong reply or replay")
	}
	second := g.mention("bob", "second")
	g.step()
	g.step()
	g.state(second, "sent")
	if len(g.replies) != 2 || !strings.Contains(g.texts[1], "小李（bob）") {
		t.Fatal("crossed subject")
	}
	third := g.mention("alice", "third")
	g.step()
	g.sql("UPDATE project_members SET role='revoked' WHERE account_id='alice'")
	g.step()
	g.state(third, "suppressed")
	if len(g.replies) != 2 {
		t.Fatal("sent after revocation")
	}
	lost := g.mention("bob", "fourth")
	g.step()
	send, err := g.claim(g.ctx)
	if err != nil || send.State != "sending" {
		t.Fatal(err)
	}
	g.sql("UPDATE agent_group_work SET lease_until=now() WHERE id=$1", lost)
	g.step()
	g.state(lost, "unknown")
	if len(g.replies) != 2 {
		t.Fatal("resent an uncertain delivery")
	}
}
