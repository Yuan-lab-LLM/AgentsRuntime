//go:build !windows

package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"syscall"
)

func ChownWorkspace(path string, uid, gid int) error {
	if uid <= 0 || gid <= 0 {
		return nil
	}
	return os.Chown(path, uid, gid)
}

func ChgrpWorkspace(path string, gid int) error {
	if gid <= 0 {
		return nil
	}
	return os.Lchown(path, -1, gid)
}

func teamDirectoryGroup(root *workspaceio.FS) (int, error) {
	info, err := root.Stat(".")
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("Team group unavailable")
	}
	return int(stat.Gid), nil
}

func nativeTeamDirectoryIdentity(root *workspaceio.FS) (uint64, uint64, error) {
	info, err := root.Stat(".")
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return 0, 0, fmt.Errorf("Team directory identity unavailable")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
