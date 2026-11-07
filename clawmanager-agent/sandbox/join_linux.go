//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"syscall"
)

func namespaceOwner(pid, user *os.File, container bool) (*os.File, error) {
	if container {
		fd, err := syscall.Dup(int(user.Fd()))
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "terminal-user-owner"), nil
	}
	fd, _, errno := syscall.Syscall(syscall.SYS_IOCTL, pid.Fd(), 0xb701, 0) // NS_GET_USERNS
	if errno != 0 {
		return nil, fmt.Errorf("pin PID namespace owner: %w", errno)
	}
	return os.NewFile(fd, "terminal-pid-owner"), nil
}
