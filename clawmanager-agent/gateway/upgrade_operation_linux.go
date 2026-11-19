//go:build linux

package gateway

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type upgradeOperationFileKey struct{}

var upgradeOperationFiles sync.Map

func (m *GatewayManager) upgradeOperationContext(ctx context.Context, instanceID int) context.Context {
	name := filepath.Join(m.upgradeRoot(), "operations", "instance-"+strconv.Itoa(instanceID)+".lock")
	if file, ok := upgradeOperationFiles.Load(name); ok {
		return context.WithValue(ctx, upgradeOperationFileKey{}, file.(*os.File))
	}
	return ctx
}

func inheritUpgradeOperationFence(ctx context.Context, cmd *exec.Cmd) {
	if file, ok := ctx.Value(upgradeOperationFileKey{}).(*os.File); ok {
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	}
}

// The lock inode is permanent: unlinking it on release would let a third
// process lock a replacement while a second process still holds the old inode.
// Linux implements flock over NFS v4 locks, so separate Runtime Pods share it.
func (m *GatewayManager) lockUpgradeOperation(instanceID int) (func(), error) {
	if instanceID <= 0 {
		return nil, ErrWorkspacePath
	}
	dir := filepath.Join(m.upgradeRoot(), "operations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, "instance-"+strconv.Itoa(instanceID)+".lock")
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("%w: another upgrade operation owns instance %d", ErrWriterLeaseActive, instanceID)
	}
	file := os.NewFile(uintptr(fd), name)
	upgradeOperationFiles.Store(name, file)
	// Close, never explicit LOCK_UN: an inherited command FD must keep the
	// fence if the Agent exits before Doctor. Release follows the last FD.
	return func() { upgradeOperationFiles.Delete(name); _ = file.Close() }, nil
}
