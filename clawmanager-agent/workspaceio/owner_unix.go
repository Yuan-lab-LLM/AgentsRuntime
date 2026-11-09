//go:build unix

package workspaceio

import (
	"fmt"
	"os"
	"syscall"
)

func ownedBySupervisor(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func ValidateInstanceCaller(uid, gid int) error {
	if uid < 200001 || uid != gid || os.Geteuid() != uid || os.Getegid() != gid {
		return fmt.Errorf("ownership preservation requires the exact instance caller")
	}
	return nil
}

func groupMatches(info os.FileInfo, gid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Gid == uint32(gid)
}
