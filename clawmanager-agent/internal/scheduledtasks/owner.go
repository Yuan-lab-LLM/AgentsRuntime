package scheduledtasks

import (
	"fmt"
	"os"
	"runtime"
)

// ownPath sets uid:gid on path when both ids are positive.
// No-op on Windows (os.Chown is unsupported for this use-case).
func ownPath(path string, uid, gid int) error {
	if uid <= 0 || gid <= 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	return nil
}
