//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package openclaw

import (
	"path/filepath"

	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"golang.org/x/sys/unix"
)

// Resolve both parents through pinned roots. Renameat operates on the directory
// descriptors, so a concurrent symlink replacement cannot redirect the move.
func renameUpgradeScopes(a *workspaceio.FS, old string, b *workspaceio.FS, new string) error {
	source, err := a.Open(filepath.Dir(old))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := b.Open(filepath.Dir(new))
	if err != nil {
		return err
	}
	defer target.Close()
	return unix.Renameat(int(source.Fd()), filepath.Base(old), int(target.Fd()), filepath.Base(new))
}
