package slots

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// This is preparation for container termination, never execution-stop proof.
// A graceful operation keeps its no-escalation policy on every retry.
func newContainerPrimaryStop(mu *sync.Mutex, terminate func(context.Context) error, kill func() error, waited <-chan struct{}, timeout time.Duration) func(context.Context, bool) error {
	signaled, forced := false, false
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return func(ctx context.Context, graceful bool) error {
		mu.Lock()
		defer mu.Unlock()
		if graceful && forced {
			return errors.New("primary shutdown was forced")
		}
		select {
		case <-waited:
			return nil
		default:
		}
		if !signaled {
			if err := terminate(ctx); err != nil && !errors.Is(err, os.ErrProcessDone) {
				if graceful {
					return err
				}
				forced = true
				if err = kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return err
				}
			}
			signaled = true
		}
		if graceful {
			select {
			case <-waited:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-waited:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		forced = true
		if err := kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-waited:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
