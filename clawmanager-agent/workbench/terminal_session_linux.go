//go:build linux

package workbench

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// Establish the PTY session after entering the primary's PID namespace. An
// outside monitor's session/process-group IDs are invisible inside that view.
func prepareSharedTerminal(input io.Reader) error {
	file, ok := input.(*os.File)
	if !ok {
		return Invalid()
	}
	if _, err := syscall.Setsid(); err != nil {
		return fmt.Errorf("create terminal session: %w", err)
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCSCTTY, 0)
	if errno != 0 {
		return fmt.Errorf("acquire terminal: %w", errno)
	}
	return nil
}
