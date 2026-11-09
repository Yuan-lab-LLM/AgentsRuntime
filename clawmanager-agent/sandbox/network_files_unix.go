//go:build linux || darwin

package sandbox

import (
	"os"
	"syscall"
)

func networkFileOwner(info os.FileInfo, uid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}
