package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type sdkProcess struct {
	command func(context.Context) *exec.Cmd
}

func sdkCommand(executable string) sdkProcess {
	return sdkProcess{command: func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, executable, WorkerCommand)
		cmd.Env = []string{}
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			switch strings.ToUpper(key) {
			case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "SYSTEMROOT":
				cmd.Env = append(cmd.Env, env)
			}
		}
		return cmd
	}}
}

// run joins the process after every exit, including cancellation and malformed
// IPC. The worker receives only its Feishu configuration.
func (p sdkProcess) run(ctx context.Context, cfg workerConfig, signal func(string), accept func(context.Context, orchestrator.InboundEvent) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := p.command(ctx)
	input, err := cmd.StdinPipe()
	if err != nil {
		return errWorker
	}
	defer input.Close()
	output, childOutput, err := os.Pipe()
	if err != nil {
		return errWorker
	}
	defer output.Close()
	defer childOutput.Close()
	// Use a private descriptor because SDK initialization writes to stdout.
	cmd.ExtraFiles = []*os.File{childOutput}
	if err := cmd.Start(); err != nil {
		return errWorker
	}
	_ = childOutput.Close()
	pulseDone := make(chan struct{})
	defer func() {
		cancel()
		<-pulseDone
		_ = cmd.Wait()
	}()

	pipe := input.(*os.File) // Cmd.StdinPipe returns the write end of an OS pipe.
	encoder := json.NewEncoder(pipe)
	if pipe.SetWriteDeadline(time.Now().Add(time.Second)) != nil || encoder.Encode(cfg) != nil {
		close(pulseDone)
		return errWorker
	}
	var writeMu sync.Mutex
	send := func(reply workerReply) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := pipe.SetWriteDeadline(time.Now().Add(sdkWriteBudget)); err != nil {
			return err
		}
		return encoder.Encode(reply)
	}
	go func() {
		defer close(pulseDone)
		ticker := time.NewTicker(parentPulseInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if send(workerReply{}) != nil {
					cancel()
					return
				}
			}
		}
	}()
	scanner := workerScanner(output)
	for scanner.Scan() {
		var frame workerFrame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return fmt.Errorf("%w: decode IPC frame", errWorker)
		}
		if ctx.Err() != nil {
			break
		}
		switch frame.Kind {
		case "ready", "pong":
			signal(frame.Kind)
		case "event":
			if frame.Event == nil || frame.ID == 0 || frame.Deadline.IsZero() {
				return errWorker
			}
			deadline := time.Now().Add(callbackBudget)
			callback, stop := context.WithDeadline(ctx, minTime(deadline, frame.Deadline))
			ok := callback.Err() == nil && accept(callback, *frame.Event) == nil && callback.Err() == nil
			stop()
			if send(workerReply{ID: frame.ID, OK: ok}) != nil {
				return errWorker
			}
		default:
			return errWorker
		}
	}
	return errWorker
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
