//go:build windows

package gateway

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func configureGatewayCommand(*exec.Cmd, int, int) {
}

func stopGatewayCommand(ctx context.Context, cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	case <-timer.C:
		return errors.New("gateway did not exit after kill")
	}
}
