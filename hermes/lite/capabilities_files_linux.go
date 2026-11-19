//go:build linux

package hermes

import (
	"os"
	"syscall"
)

func desktopReleaseOwnerTrusted(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
