//go:build !windows

package gateway

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureGatewayCommand(cmd *exec.Cmd, uid, gid int) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if uid > 0 && gid > 0 {
		attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	cmd.SysProcAttr = attr
}

func stopGatewayCommand(ctx context.Context, cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		if signalErr := cmd.Process.Signal(syscall.SIGTERM); signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
			return signalErr
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		return ctx.Err()
	case <-done:
		// The group can outlive its leader. A successful wait alone does not
		// prove that inherited listeners or tool subprocesses have stopped.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	case <-timer.C:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("gateway did not exit after kill")
		}
	}
}
