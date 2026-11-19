//go:build linux

package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"syscall"
)

func protectNativeStorageHelper() error {
	// The trusted helper shares an instance UID with native subprocesses. Prevent
	// those processes recovering its verification channel through proc or ptrace.
	_, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 4, 0, 0, 0, 0, 0)
	if err != 0 {
		return fmt.Errorf("protect native helper process: %w", err)
	}
	syscall.CloseOnExec(0)
	syscall.CloseOnExec(1)
	return nil
}

// Nonblocking/no-follow open followed by fstat also rejects a FIFO or symlink
// substituted after discovery. Directories remain valid discovery inputs.
func openNativeSkillInput(root *workspaceio.FS, path string, directory bool) (*os.File, error) {
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if os.Geteuid() >= 200001 && (!ok || stat.Uid != uint32(os.Geteuid())) {
		file.Close()
		return nil, fmt.Errorf("foreign private skill inode")
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		file.Close()
		return nil, fmt.Errorf("invalid skill input type")
	}
	return file, nil
}
