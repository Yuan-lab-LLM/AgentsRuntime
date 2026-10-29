//go:build unix

package workspaceio

import (
	"os"
	"syscall"
)

func ownedBySupervisor(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
