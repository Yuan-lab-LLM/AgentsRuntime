//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package openclaw

import (
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

func renameUpgradeScopes(a *workspaceio.FS, old string, b *workspaceio.FS, new string) error {
	return errors.New("cross-scope native upgrade rename is unsupported on this platform")
}
