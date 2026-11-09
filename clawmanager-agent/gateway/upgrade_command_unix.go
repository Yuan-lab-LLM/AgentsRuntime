//go:build !windows

package gateway

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Doctor can spawn plugin checks. Cancelling only its main process leaves
// children writing after the operation fence is released. Always terminate the
// private process group, including helpers left behind after a successful exit.
func fenceUpgradeCommand(cmd *exec.Cmd) func() {
	cmd.WaitDelay = 5 * time.Second
	kill := func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = kill
	return func() { _ = kill() }
}
