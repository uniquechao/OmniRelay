package feishu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

// These budgets cover the callback, including IPC and database commit, and
// leave time for the SDK to write the platform response.
const (
	callbackBudget      = 2300 * time.Millisecond
	sdkWriteBudget      = 400 * time.Millisecond
	maxWorkerFrame      = 1 << 20
	parentPulseInterval = 2 * time.Second
	parentPulseTimeout  = 8 * time.Second
)

const WorkerCommand = "feishu-sdk-worker"
const workerOutputFD = 3

type workerFrame struct {
	Kind     string
	ID       uint64                     `json:",omitempty"`
	Deadline time.Time                  `json:",omitzero"`
	Event    *orchestrator.InboundEvent `json:",omitempty"`
}

type workerReply struct {
	ID uint64
	OK bool
}

var errWorker = errors.New("Feishu SDK worker unavailable")

// ServeSDKWorker owns a single physical connection. Its executable exits on
// return, reclaiming the upstream SDK's process-lifetime cache goroutine.
func ServeSDKWorker(ctx context.Context) error {
	return serveInheritedSDKWorker(ctx)
}

func serveInheritedSDKWorker(ctx context.Context, options ...larkws.ClientOption) error {
	output := os.NewFile(workerOutputFD, "feishu-sdk-output")
	if output == nil {
		return errWorker
	}
	defer output.Close()
	return serveSDKWorker(ctx, os.Stdin, output, options...)
}

func serveSDKWorker(ctx context.Context, input io.Reader, output io.Writer, options ...larkws.ClientOption) error {
	scanner := workerScanner(input)
	var cfg workerConfig
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &cfg) != nil ||
		cfg.AppID == "" || cfg.AppSecret == "" || cfg.TenantKey == "" || cfg.BotOpenID == "" {
		return errWorker
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bridge := &workerBridge{out: make(chan workerFrame, 8), ready: make(chan struct{})}
	pulses := make(chan struct{}, 1)
	go func() {
		defer cancel()
		waitForParent(ctx, pulses, parentPulseTimeout)
	}()
	go func() {
		defer cancel()
		for scanner.Scan() {
			var reply workerReply
			if json.Unmarshal(scanner.Bytes(), &reply) != nil {
				return
			}
			if reply.ID == 0 {
				select {
				case pulses <- struct{}{}:
				default:
				}
			} else {
				bridge.reply(reply)
			}
		}
	}()
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-bridge.out:
				data, err := json.Marshal(frame)
				if err != nil || len(data)+1 >= maxWorkerFrame {
					bridge.reply(workerReply{ID: frame.ID})
					continue
				}
				if _, err := output.Write(append(data, '\n')); err != nil {
					return
				}
			}
		}
	}()

	events := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(
		func(callback context.Context, incoming *im.P2MessageReceiveV1) error {
			received := time.Now()
			event, ok := mapInbound(cfg, incoming, received)
			if !ok {
				return nil
			}
			return bridge.deliver(callback, event, received.Add(callbackBudget))
		})
	opts := []larkws.ClientOption{
		larkws.WithEventHandler(events), larkws.WithAutoReconnect(false),
		larkws.WithLogger(pongLogger{signal: func() { bridge.signal("pong") }}),
		larkws.WithWriteTimeout(sdkWriteBudget),
		larkws.WithHttpClient(&http.Client{
			Timeout:       8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
		larkws.WithOnReady(func() {
			select {
			case bridge.out <- workerFrame{Kind: "ready"}:
				close(bridge.ready)
			case <-ctx.Done():
			}
		}),
		larkws.WithOnDisconnected(cancel),
	}
	client := larkws.NewClient(cfg.AppID, cfg.AppSecret, append(opts, options...)...)
	if err := client.Start(ctx); err != nil {
		return errWorker
	}
	return nil
}

// The parent can be suspended while the SDK process keeps running. Exit before
// the Kubernetes Lease can elapse without renewal by the parent.
func waitForParent(ctx context.Context, pulses <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-pulses:
			timer.Reset(timeout)
		}
	}
}

func workerScanner(source io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 4096), maxWorkerFrame)
	return scanner
}

type workerBridge struct {
	out   chan workerFrame
	ready chan struct{}

	mu      sync.Mutex
	id      uint64
	pending chan bool
}

func (b *workerBridge) signal(kind string) {
	select {
	case b.out <- workerFrame{Kind: kind}:
	default:
	}
}

func (b *workerBridge) deliver(ctx context.Context, event orchestrator.InboundEvent, deadline time.Time) error {
	if len(event.Text) >= maxWorkerFrame {
		return errWorker
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-b.ready:
	case <-ctx.Done():
		return errWorker
	}
	b.mu.Lock()
	// Cap pending SDK callbacks and fail excess deliveries within the ACK budget.
	if b.pending != nil {
		b.mu.Unlock()
		return errWorker
	}
	b.id++
	id := b.id
	reply := make(chan bool, 1)
	b.pending = reply
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.pending = nil
		b.mu.Unlock()
	}()

	select {
	case b.out <- workerFrame{Kind: "event", ID: id, Deadline: deadline, Event: &event}:
	case <-ctx.Done():
		return errWorker
	}
	select {
	case ok := <-reply:
		if ok && ctx.Err() == nil {
			return nil
		}
	case <-ctx.Done():
	}
	return errWorker
}

func (b *workerBridge) reply(reply workerReply) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending != nil && reply.ID == b.id {
		select {
		case b.pending <- reply.OK:
		default:
		}
	}
}

// v3.13.0 reports a parsed application-level Pong through this SDK logger
// callback. Consume the signal only. SDK log arguments can contain secrets.
type pongLogger struct{ signal func() }

func (l pongLogger) Debug(_ context.Context, args ...any) {
	if len(args) > 0 {
		message, _ := args[0].(string)
		if message == "receive pong" || strings.HasPrefix(message, "receive pong [conn_id=") {
			l.signal()
		}
	}
}
func (pongLogger) Info(context.Context, ...any)  {}
func (pongLogger) Warn(context.Context, ...any)  {}
func (pongLogger) Error(context.Context, ...any) {}
