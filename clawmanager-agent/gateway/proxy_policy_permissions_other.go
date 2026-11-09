//go:build !linux

package gateway

import (
	"fmt"
	"os"
	"runtime"
)

// Preserve the existing non-Linux development contract. Managed deployment
// and isolated-UID permission acceptance execute on Linux.
func ensureProxyPolicyDirectory(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0) {
		return fmt.Errorf("unsafe proxy policy directory")
	}
	return nil
}
