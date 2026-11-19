package slots

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

func waitForSlotOwnership(ctx context.Context, guard *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := lockWorkspace(guard); err == nil {
			return nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		// A coordinator may be persisting an offline stop receipt. Waiting here
		// avoids turning a normal ownership transfer into kubelet restart backoff.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
