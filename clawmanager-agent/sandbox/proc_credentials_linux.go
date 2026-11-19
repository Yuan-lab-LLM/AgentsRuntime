//go:build linux

package sandbox

import (
	"fmt"
	"runtime"
	"syscall"
)

// CAP_SYS_PTRACE permits proc's ptrace checks, but it does not bypass the 0500
// business-owned fd directory. Enumerate only this pinned process's descriptors
// with its filesystem UID. No arbitrary callback or filesystem path is exposed.
func readProcessDescriptors(root string, pid int, uid uint32, scoped bool) ([]string, error) {
	if !scoped {
		return processDescriptorLinks(root, pid)
	}
	type result struct {
		links []string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		// Returning with the thread still locked makes Go discard the thread if
		// credentials cannot be restored; it must never reenter the thread pool.
		current := func() uint32 {
			value, _, _ := syscall.RawSyscall(syscall.SYS_SETFSUID, uintptr(^uint32(0)), 0, 0)
			return uint32(value)
		}
		previous := current()
		_, _, _ = syscall.RawSyscall(syscall.SYS_SETFSUID, uintptr(uid), 0, 0)
		if current() != uid {
			_, _, _ = syscall.RawSyscall(syscall.SYS_SETFSUID, uintptr(previous), 0, 0)
			if current() == previous {
				runtime.UnlockOSThread()
			}
			done <- result{err: fmt.Errorf("business proc filesystem credentials unavailable")}
			return
		}
		links, err := processDescriptorLinks(root, pid)
		_, _, _ = syscall.RawSyscall(syscall.SYS_SETFSUID, uintptr(previous), 0, 0)
		if current() != previous {
			done <- result{err: fmt.Errorf("cannot restore proc filesystem credentials")}
			return
		}
		runtime.UnlockOSThread()
		done <- result{links: links, err: err}
	}()
	r := <-done
	return r.links, r.err
}
