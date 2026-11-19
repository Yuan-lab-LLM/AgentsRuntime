package sandbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// SignalProcess signals only an already pinned process. Trusted managers with
// SETUID but no outer KILL capability may temporarily use the known business
// UID; this never grants teardown proof or a numeric-PID fallback.
func SignalProcess(process *os.Process, uid uint32, signal os.Signal) error {
	if process == nil || uid == 0 || uid == ^uint32(0) {
		return fmt.Errorf("invalid pinned process signal identity")
	}
	if err := process.WithHandle(func(uintptr) {}); err != nil {
		return fmt.Errorf("pinned process signal unavailable: %w", err)
	}
	err := process.Signal(signal)
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	return signalProcessAsUID(process, uid, signal)
}
