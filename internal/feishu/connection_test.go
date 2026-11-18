package feishu

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/system"
)

type testDial func(context.Context, workerConfig, func(string), func(context.Context, orchestrator.InboundEvent) error) error

func (f testDial) run(ctx context.Context, cfg workerConfig, signal func(string),
	accept func(context.Context, orchestrator.InboundEvent) error) error {
	return f(ctx, cfg, signal, accept)
}

type testBotInfo struct{}

func (testBotInfo) TenantToken(context.Context) (string, system.CheckError) {
	return "fixture", ""
}
func (testBotInfo) BotInfo(context.Context, string) (system.BotInfo, system.CheckError) {
	return system.BotInfo{OpenID: "ou_bot"}, ""
}

type testInbox func(context.Context, orchestrator.InboundEvent) (orchestrator.AcceptResult, error)

func (f testInbox) AcceptInbound(ctx context.Context, event orchestrator.InboundEvent) (orchestrator.AcceptResult, error) {
	return f(ctx, event)
}

func TestConnectionRestartsAndFencesLateCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	type session struct {
		ctx    context.Context
		accept func(context.Context, orchestrator.InboundEvent) error
	}
	sessions := make(chan session, 4)
	receipts := make(chan orchestrator.ConnectionFence, 4)
	store := &connectionStoreFixture{enabled: true, states: make(chan string, 32)}
	manager := &Connection{
		store: store, check: testBotInfo{}, ttl: 3 * time.Second, poll: 20 * time.Millisecond,
		startLimit: time.Second, silence: 350 * time.Millisecond,
		inbox: testInbox(func(_ context.Context, event orchestrator.InboundEvent) (orchestrator.AcceptResult, error) {
			receipts <- event.Connection
			return orchestrator.AcceptResult{WorkID: "fixture"}, nil
		}),
		runner: testDial(func(ctx context.Context, cfg workerConfig, signal func(string),
			accept func(context.Context, orchestrator.InboundEvent) error) error {
			if cfg.BotOpenID != "ou_bot" {
				t.Error("SDK worker did not receive the verified Bot open_id")
			}
			sessions <- session{ctx, accept}
			signal("ready")
			<-ctx.Done()
			return errWorker
		}),
	}
	finished := make(chan error, 1)
	go func() {
		finished <- manager.Run(ctx, func(ctx context.Context) error { return ctx.Err() })
	}()
	state := func(want string) {
		t.Helper()
		for receive(t, ctx, store.states) != want {
		}
	}
	first := receive(t, ctx, sessions)
	state("connected")
	if err := first.accept(ctx, orchestrator.InboundEvent{}); err != nil {
		t.Fatal(err)
	}
	firstFence := receive(t, ctx, receipts)
	state("reconnecting") // A ready socket with no subsequent Pong times out.
	receive(t, ctx, first.ctx.Done())
	if err := first.accept(ctx, orchestrator.InboundEvent{}); !errors.Is(err, orchestrator.ErrStaleConnection) {
		t.Fatalf("late callback reached admission: %v", err)
	}

	second := receive(t, ctx, sessions)
	state("connected")
	if err := second.accept(ctx, orchestrator.InboundEvent{}); err != nil {
		t.Fatal(err)
	}
	if fence := receive(t, ctx, receipts); fence.ConnectionGeneration <= firstFence.ConnectionGeneration {
		t.Fatal("reconnect reused a generation")
	}
	store.change(false, false)
	receive(t, ctx, second.ctx.Done())
	select {
	case <-sessions:
		t.Fatal("disabled Bot started another SDK worker")
	case <-time.After(60 * time.Millisecond):
	}
	store.change(true, false)
	third := receive(t, ctx, sessions)
	state("connected")
	store.change(true, true)
	receive(t, ctx, third.ctx.Done())
	if err := receive(t, ctx, finished); !errors.Is(err, system.ErrStaleGeneration) {
		t.Fatalf("rotated credentials did not relinquish ownership: %v", err)
	}
}

type connectionStoreFixture struct {
	mu      sync.Mutex
	enabled bool
	rotated bool
	fence   orchestrator.ConnectionFence
	states  chan string
}

func (s *connectionStoreFixture) change(enabled, rotated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled, s.rotated = enabled, rotated
	s.fence.ConfigVersion++
}

func (s *connectionStoreFixture) ConnectionFence(context.Context) (orchestrator.ConnectionFence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rotated {
		return s.fence, system.ErrStaleGeneration
	}
	if !s.enabled {
		return s.fence, orchestrator.ErrStaleConnection
	}
	return s.fence, nil
}

func (s *connectionStoreFixture) StartConnection(_ context.Context, snapshot orchestrator.ConnectionFence) (orchestrator.ConnectionFence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.rotated || snapshot != s.fence {
		return s.fence, orchestrator.ErrStaleConnection
	}
	s.fence.ConnectionGeneration++
	return s.fence, nil
}

func (s *connectionStoreFixture) KeepConnecting(_ context.Context, f orchestrator.ConnectionFence) error {
	return s.record(f, "connecting")
}

func (s *connectionStoreFixture) RenewConnection(_ context.Context, f orchestrator.ConnectionFence) error {
	return s.record(f, "connected")
}

func (s *connectionStoreFixture) MarkReconnecting(_ context.Context, f orchestrator.ConnectionFence) error {
	return s.record(f, "reconnecting")
}

func (s *connectionStoreFixture) EndConnection(_ context.Context, f orchestrator.ConnectionFence) error {
	return s.record(f, "unavailable")
}

func (s *connectionStoreFixture) record(f orchestrator.ConnectionFence, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.rotated || f != s.fence {
		return orchestrator.ErrStaleConnection
	}
	select {
	case s.states <- state:
	default:
	}
	return nil
}
