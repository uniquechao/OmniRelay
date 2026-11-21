package feishu

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/uniquechao/OmniRelay/internal/orchestrator"
	"github.com/uniquechao/OmniRelay/internal/system"
)

type ConnectionStore interface {
	ConnectionFence(context.Context) (orchestrator.ConnectionFence, error)
	StartConnection(context.Context, orchestrator.ConnectionFence) (orchestrator.ConnectionFence, error)
	KeepConnecting(context.Context, orchestrator.ConnectionFence) error
	RenewConnection(context.Context, orchestrator.ConnectionFence) error
	MarkReconnecting(context.Context, orchestrator.ConnectionFence) error
	EndConnection(context.Context, orchestrator.ConnectionFence) error
}

type connectionRunner interface {
	run(context.Context, workerConfig, func(string), func(context.Context, orchestrator.InboundEvent) error) error
}

type Connection struct {
	config     workerConfig
	check      system.Provider
	store      ConnectionStore
	inbox      orchestrator.InboxAcceptor
	archive    func(context.Context, orchestrator.InboundEvent) error
	intercept  func(context.Context, orchestrator.InboundEvent) (bool, error)
	runner     connectionRunner
	ttl        time.Duration
	poll       time.Duration
	startLimit time.Duration
	silence    time.Duration
}

func NewConnection(executable, tenantKey string, check *BotCheck, store ConnectionStore,
	inbox orchestrator.InboxAcceptor, ttl time.Duration) *Connection {
	return &Connection{
		config: workerConfig{AppID: check.appID, AppSecret: check.secret, TenantKey: tenantKey},
		check:  check, store: store, inbox: inbox, runner: sdkCommand(executable),
		ttl: ttl, poll: min(time.Second, max(10*time.Millisecond, ttl/4)), startLimit: 40 * time.Second,
		// v3.13.0 defaults to a 120s Ping, with a read deadline of two
		// intervals plus five seconds. The SDK may time out sooner.
		silence: 245 * time.Second,
	}
}

// SetArchive wires the independent archive before the connection is started.
func (c *Connection) SetArchive(accept func(context.Context, orchestrator.InboundEvent) error) {
	c.config.Archive = accept != nil
	c.archive = accept
}

func (c *Connection) SetInboundInterceptor(accept func(context.Context, orchestrator.InboundEvent) (bool, error), observe bool) {
	c.intercept = accept
	c.config.ObserveReceipts = observe
}

func (c *Connection) accept(ctx context.Context, event orchestrator.InboundEvent) error {
	if c.intercept != nil {
		handled, err := c.intercept(ctx, event)
		if handled || err != nil {
			return err
		}
	}
	var archiveErr, commandErr error
	if event.Archive == nil || event.MessageID != "" {
		command := event
		command.Archive = nil
		_, commandErr = c.inbox.AcceptInbound(ctx, command)
	}
	if event.Archive != nil && c.archive != nil {
		archiveErr = c.archive(ctx, event)
	}
	return errors.Join(archiveErr, commandErr)
}

// Run executes only while the Kubernetes elector grants ownership. Verify is
// called after taking the activation snapshot so an old holder cannot replace
// a newer generation using a snapshot it read after losing the Lease.
func (c *Connection) Run(ctx context.Context, verify func(context.Context) error) error {
	var fence orchestrator.ConnectionFence
	defer func() { c.end(fence) }()

	delay := time.Second
	for ctx.Err() == nil {
		op, stop := c.operation(ctx)
		snapshot, err := c.store.ConnectionFence(op)
		if err == nil {
			if err = verify(op); err != nil {
				stop()
				return err
			}
			if op.Err() == nil {
				var next orchestrator.ConnectionFence
				next, err = c.store.StartConnection(op, snapshot)
				if err == nil {
					fence = next
				}
			} else {
				err = op.Err()
			}
		}
		stop()
		if errors.Is(err, system.ErrStaleGeneration) {
			return err
		}
		if err != nil {
			c.end(fence)
			fence = orchestrator.ConnectionFence{}
			sleepContext(ctx, c.poll)
			continue
		}

		healthy, err := c.attempt(ctx, fence, verify)
		if ctx.Err() != nil || errors.Is(err, errLeaseLost) {
			return err
		}
		if !errors.Is(err, errWorker) {
			c.end(fence)
			fence = orchestrator.ConnectionFence{}
			sleepContext(ctx, c.poll)
			continue
		}
		op, stop = c.operation(ctx)
		_ = c.store.MarkReconnecting(op, fence)
		stop()
		if healthy {
			delay = time.Second
		}
		jitter := time.Duration(rand.Int64N(int64(delay/2) + 1))
		sleepContext(ctx, delay/2+jitter)
		delay = min(delay*2, 15*time.Second)
	}
	return ctx.Err()
}

func (c *Connection) attempt(parent context.Context, fence orchestrator.ConnectionFence, verify func(context.Context) error) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var runErr error
	signals := make(chan string, 8)
	admitting := make(chan struct{})
	go func() {
		defer close(done)
		cfg := c.config
		token, checkErr := c.check.TenantToken(ctx)
		if checkErr != "" {
			runErr = errWorker
			return
		}
		info, checkErr := c.check.BotInfo(ctx, token)
		if checkErr != "" {
			runErr = errWorker
			return
		}
		cfg.BotOpenID = info.OpenID
		op, stop := c.operation(ctx)
		err := verify(op)
		stop()
		if err != nil {
			runErr = err
			return
		}
		runErr = c.runner.run(ctx, cfg, func(signal string) {
			select {
			case signals <- signal:
			case <-ctx.Done():
			}
		}, func(callback context.Context, event orchestrator.InboundEvent) error {
			select {
			case <-admitting:
			case <-ctx.Done():
				return orchestrator.ErrStaleConnection
			case <-callback.Done():
				return orchestrator.ErrStaleConnection
			}
			if callback.Err() != nil || ctx.Err() != nil {
				return orchestrator.ErrStaleConnection
			}
			event.Connection = fence
			return c.accept(callback, event)
		})
	}()
	defer func() {
		cancel()
		<-done
	}()

	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	started := time.Now()
	var lastSignal, healthySince time.Time
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-done:
			return !healthySince.IsZero() && time.Since(healthySince) >= c.ttl, runErr
		case signal := <-signals:
			lastSignal = time.Now()
			if signal == "ready" && healthySince.IsZero() {
				if err := c.publish(ctx, fence, verify, true); err != nil {
					return false, err
				}
				healthySince = time.Now()
				close(admitting)
			}
		case <-ticker.C:
			ready := !healthySince.IsZero()
			if (!ready && time.Since(started) >= c.startLimit) || (ready && time.Since(lastSignal) >= c.silence) {
				return ready && time.Since(healthySince) >= c.ttl, errWorker
			}
			if err := c.publish(ctx, fence, verify, ready); err != nil {
				return false, err
			}
		}
	}
}

func (c *Connection) publish(ctx context.Context, fence orchestrator.ConnectionFence,
	verify func(context.Context) error, ready bool) error {
	op, stop := c.operation(ctx)
	defer stop()
	if err := verify(op); err != nil {
		return err
	}
	if ready {
		return c.store.RenewConnection(op, fence)
	}
	return c.store.KeepConnecting(op, fence)
}

func (c *Connection) operation(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, min(2*time.Second, max(10*time.Millisecond, c.ttl/2)))
}

func (c *Connection) end(fence orchestrator.ConnectionFence) {
	if fence.ConnectionGeneration != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.store.EndConnection(ctx, fence)
	}
}

func sleepContext(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
