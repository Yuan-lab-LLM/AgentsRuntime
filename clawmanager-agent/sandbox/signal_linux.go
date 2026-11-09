//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func processLaunchUID(cmd *exec.Cmd) uint32 {
	if cmd != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		return cmd.SysProcAttr.Credential.Uid
	}
	return 0
}

func signalProcessAsUID(process *os.Process, uid uint32, signal os.Signal) error {
	if process == nil || uid == 0 || uid == ^uint32(0) {
		return fmt.Errorf("invalid scoped signal identity")
	}
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		current := func() uint32 { value, _, _ := syscall.RawSyscall(syscall.SYS_GETEUID, 0, 0, 0); return uint32(value) }
		set := func(value uint32) error {
			_, _, errno := syscall.RawSyscall(syscall.SYS_SETRESUID, uintptr(^uint32(0)), uintptr(value), uintptr(^uint32(0)))
			if errno != 0 {
				return errno
			}
			return nil
		}
		previous := current()
		if err := set(uid); err != nil {
			runtime.UnlockOSThread()
			result <- fmt.Errorf("set pinned signal identity: %w", err)
			return
		}
		if current() != uid {
			_ = set(previous)
			if current() == previous {
				runtime.UnlockOSThread()
			}
			result <- fmt.Errorf("pinned signal identity did not change")
			return
		}
		err := process.Signal(signal)
		restoreErr := set(previous)
		if restoreErr != nil || current() != previous {
			// Keep the thread locked. Go destroys it when this goroutine exits,
			// rather than admitting a thread with changed credentials to the pool.
			result <- errors.Join(err, fmt.Errorf("cannot restore pinned signal identity: %v", restoreErr))
			return
		}
		runtime.UnlockOSThread()
		result <- err
	}()
	return <-result
}
