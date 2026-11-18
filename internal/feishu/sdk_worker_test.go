package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

var fixtureConfig = workerConfig{
	AppID: "cli_fixture", AppSecret: "fixture_secret", TenantKey: "tenant_fixture", BotOpenID: "ou_bot",
}

const fixtureEvent = `{"schema":"2.0","header":{"event_id":"evt_fixture","event_type":"im.message.receive_v1","app_id":"cli_fixture","tenant_key":"tenant_fixture"},
"event":{"sender":{"sender_id":{"open_id":"ou_human"},"sender_type":"user","tenant_key":"tenant_fixture"},
"message":{"message_id":"om_fixture","chat_id":"oc_fixture","chat_type":"group","thread_id":"omt_fixture",
"parent_id":"om_parent","root_id":"om_root","message_type":"text","content":"{\"text\":\"  查询进度  \"}",
"mentions":[{"id":{"open_id":"ou_bot"},"mentioned_type":"bot","tenant_key":"tenant_fixture"}]}}}`

func TestTrustedMessageMapping(t *testing.T) {
	cases := []struct {
		name, old, replacement string
		kind                   orchestrator.ChatKind
	}{
		{name: "group", kind: orchestrator.ChatGroup},
		{"private", `"chat_type":"group"`, `"chat_type":"p2p"`, orchestrator.ChatPrivate},
		{"wrong application", `"app_id":"cli_fixture"`, `"app_id":"wrong"`, ""},
		{"wrong event tenant", `"tenant_key":"tenant_fixture"`, `"tenant_key":"wrong"`, ""},
		{"wrong sender tenant", `"sender_type":"user","tenant_key":"tenant_fixture"`, `"sender_type":"user","tenant_key":"external"`, ""},
		{"bot sender", `"sender_type":"user"`, `"sender_type":"bot"`, ""},
		{"self sender", `"open_id":"ou_human"`, `"open_id":"ou_bot"`, ""},
		{"another mention", `"open_id":"ou_bot"`, `"open_id":"ou_other"`, ""},
		{"no mention", `[{"id":{"open_id":"ou_bot"},"mentioned_type":"bot","tenant_key":"tenant_fixture"}]`, `[]`, ""},
		{"external mention", `"mentioned_type":"bot","tenant_key":"tenant_fixture"`, `"mentioned_type":"bot","tenant_key":"external"`, ""},
		{"wrong event", `im.message.receive_v1`, `im.message.recalled_v1`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := fixtureEvent
			if tc.old != "" {
				payload = strings.Replace(payload, tc.old, tc.replacement, 1)
			}
			var incoming im.P2MessageReceiveV1
			if err := json.Unmarshal([]byte(payload), &incoming); err != nil {
				t.Fatal(err)
			}
			received := time.Now()
			event, valid := mapInbound(fixtureConfig, &incoming, received)
			if valid != (tc.kind != "") {
				t.Fatalf("valid = %v", valid)
			}
			if valid && (event.ChatKind != tc.kind || event.ThreadID != "omt_fixture" ||
				event.ReplyToMessageID != "om_parent" || event.MessageID != "om_fixture" ||
				event.SenderOpenID != "ou_human" || event.Text != "  查询进度  " || !event.ReceivedAt.Equal(received)) {
				t.Fatalf("incorrect mapping: %+v", event)
			}
		})
	}
}

// The test binary's private child entry uses the same SDK worker as main.
func TestSDKChildEntrypoint(t *testing.T) {
	domain := os.Getenv("OMNIRELAY_TEST_SDK_CHILD_DOMAIN")
	if domain == "" {
		return
	}
	if serveInheritedSDKWorker(t.Context(), larkws.WithDomain(domain)) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSDKWorkerWireACKAndReap(t *testing.T) {
	socket := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case larkws.GenEndpointUri:
			_ = json.NewEncoder(w).Encode(larkws.EndpointResp{Data: &larkws.Endpoint{
				Url:          "ws://" + r.Host + "/socket?service_id=1&device_id=fixture",
				ClientConfig: &larkws.ClientConfig{PingInterval: 120},
			}})
		case "/socket":
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				socket <- conn
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	var child *exec.Cmd
	process := sdkProcess{command: func(run context.Context) *exec.Cmd {
		// The test adapter must honor the inner cancellation used by sdkProcess.
		child = exec.CommandContext(run, os.Args[0], "-test.run=^TestSDKChildEntrypoint$")
		child.Env = append(os.Environ(), "OMNIRELAY_TEST_SDK_CHILD_DOMAIN="+server.URL)
		return child
	}}
	seen, release := make(chan string, 1), make(chan struct{})
	signals, done := make(chan string, 8), make(chan struct{})
	var workerErr error
	go func() {
		defer close(done)
		workerErr = process.run(ctx, fixtureConfig, func(signal string) { signals <- signal },
			func(callback context.Context, event orchestrator.InboundEvent) error {
				switch event.MessageID {
				case "om_success":
					seen <- event.MessageID
					select {
					case <-release:
						return nil
					case <-callback.Done():
						return callback.Err()
					}
				case "om_database_failure":
					return orchestrator.ErrInboxUnavailable
				case "om_timeout":
					<-callback.Done()
					return nil // A late result must still receive a failure ACK.
				default:
					t.Errorf("unexpected admission: %s", event.MessageID)
					return orchestrator.ErrInvalidInbound
				}
			})
	}()

	var conn *websocket.Conn
	select {
	case conn = <-socket:
	case <-done:
		t.Fatalf("SDK worker exited before dialing: %v (%v)", workerErr, child.ProcessState)
	case <-ctx.Done():
		t.Fatal("SDK worker never reached the local peer")
	}
	defer conn.Close()
	readDone := make(chan struct{})
	wire := make(chan larkws.Frame, 8)
	go func() {
		defer close(readDone)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame larkws.Frame
			if err := frame.Unmarshal(data); err != nil {
				t.Errorf("decode SDK response: %v", err)
				return
			}
			select {
			case wire <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	if got := receive(t, ctx, signals); got != "ready" {
		t.Fatalf("first signal = %s", got)
	}
	ping := receive(t, ctx, wire)
	if got := larkws.Headers(ping.Headers).GetString(larkws.HeaderType); got != "ping" {
		t.Fatalf("first SDK frame = %s", got)
	}
	ping.Headers[0].Value = "pong"
	writeSDKFrame(t, conn, ping)
	if got := receive(t, ctx, signals); got != "pong" {
		t.Fatalf("heartbeat signal = %s", got)
	}
	deliver := func(id string) time.Time {
		start := time.Now()
		writeSDKFrame(t, conn, larkws.Frame{
			Method: int32(larkws.FrameTypeData), Service: 1, Payload: []byte(strings.Replace(fixtureEvent, "om_fixture", id, 1)),
			Headers: []larkws.Header{{Key: larkws.HeaderType, Value: "event"}, {Key: larkws.HeaderMessageID, Value: id}},
		})
		return start
	}
	assertACK := func(id string, code int, started time.Time) {
		t.Helper()
		frame := receive(t, ctx, wire)
		headers := larkws.Headers(frame.Headers)
		var response larkws.Response
		err := json.Unmarshal(frame.Payload, &response)
		bizMillis, bizErr := strconv.Atoi(headers.GetString(larkws.HeaderBizRt))
		elapsed := time.Since(started)
		if err != nil || bizErr != nil || headers.GetString(larkws.HeaderMessageID) != id ||
			response.StatusCode != code || elapsed >= 3*time.Second || bizMillis >= 3000 {
			t.Fatalf("invalid SDK ACK for %s, code=%d biz_ms=%d elapsed=%v: %v %v", id, response.StatusCode, bizMillis, elapsed, err, bizErr)
		}
		t.Logf("%s: code=%d sdk_ms=%d websocket_elapsed=%v", id, code, bizMillis, elapsed)
	}

	start := deliver("om_success")
	receive(t, ctx, seen)
	select {
	case <-wire:
		t.Fatal("SDK confirmed before durable admission returned")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	assertACK("om_success", http.StatusOK, start)
	assertACK("om_database_failure", http.StatusInternalServerError, deliver("om_database_failure"))
	assertACK("om_timeout", http.StatusInternalServerError, deliver("om_timeout"))
	assertACK("om_success", http.StatusOK, deliver("om_success"))
	cancel()
	wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	receive(t, wait, done)
	receive(t, wait, readDone)
	if child.ProcessState == nil {
		t.Fatal("SDK worker was not reaped")
	}
}

func writeSDKFrame(t *testing.T, conn *websocket.Conn, frame larkws.Frame) {
	t.Helper()
	encoded, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
		t.Fatal(err)
	}
}

func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case item := <-ch:
		return item
	case <-ctx.Done():
		t.Fatal("timed out waiting for test signal")
		var zero T
		return zero
	}
}

func TestSDKWorkerParentPulseExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	pulses := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForParent(ctx, pulses, 150*time.Millisecond)
	}()
	for range 3 {
		select {
		case <-done:
			t.Fatal("active parent supervision expired")
		case <-time.After(50 * time.Millisecond):
			pulses <- struct{}{}
		}
	}
	receive(t, ctx, done)
}
