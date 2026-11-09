//go:build linux

package gateway

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// The closed channel is reusable across retries and independent of the legacy
// Done consumer. Only send SIGTERM once: never signal a recycled PID on retry.
func newGracefulGatewayStop(cmd *exec.Cmd, exited <-chan struct{}) func(context.Context) error {
	var mu sync.Mutex
	sent := false
	confirmed := false
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if confirmed {
			return nil
		}
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return ErrGatewayStopUnsupported
		}
		pid := cmd.Process.Pid
		if !sent {
			select {
			case <-exited:
				// Do not signal a group whose leader has already exited. Surviving
				// children must exit on their own; an upgrade never forces them.
			default:
				if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
			}
			sent = true
		}
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case <-exited:
				err := syscall.Kill(-pid, 0)
				if errors.Is(err, syscall.ESRCH) {
					confirmed = true
					return nil
				}
				if err != nil {
					return err
				}
			default:
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
}
