package slots

import (
	"context"
	"errors"
	"os"
	"sync"
)

// The monitor's exit alone is insufficient: confirmation must also prove the
// pinned namespace is gone. A timeout never kills the monitor or re-signals a
// possibly recycled primary process on retry.
func newSandboxGracefulStop(mu *sync.Mutex, terminate, confirm func(context.Context) error, waited <-chan struct{}) func(context.Context) error {
	signaled, confirmed := false, false
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if confirmed {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-waited:
		default:
			if !signaled {
				if err := terminate(ctx); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return err
				}
				signaled = true
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-waited:
			}
		}
		if err := confirm(ctx); err != nil {
			return err
		}
		confirmed = true
		return nil
	}
}

// Network failure and durable idle acceptance share one lock: a failure can
// never escalate after persistence has accepted graceful-only ownership.
type sandboxStopPolicy struct {
	mu   sync.Mutex
	idle bool
}

func (p *sandboxStopPolicy) fenceIdle(persist func() error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if persist != nil {
		if err := persist(); err != nil {
			return err
		}
	}
	p.idle = true
	return nil
}
func (p *sandboxStopPolicy) networkFailure(kill func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.idle {
		kill()
	}
}
