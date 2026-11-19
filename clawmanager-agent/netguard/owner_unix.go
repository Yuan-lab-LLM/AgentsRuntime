//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package netguard

import (
	"os"
	"syscall"
)

func ownedBy(info os.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}

func fileReceipt(info os.FileInfo) any {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return map[string]uint64{"device": uint64(stat.Dev), "inode": uint64(stat.Ino)}
}
