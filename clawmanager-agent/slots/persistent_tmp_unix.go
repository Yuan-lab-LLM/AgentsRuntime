//go:build linux || darwin

package slots

import (
	"fmt"
	"os"
	"syscall"
)

func prepareTmpDirectory(file *os.File, created, leaf bool, uid, gid int) error {
	if created && leaf {
		if err := file.Chown(uid, gid); err != nil {
			return err
		}
		if err := file.Chmod(os.ModeSticky | 0777); err != nil {
			return err
		}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return fmt.Errorf("persistent tmp requires a real directory")
	}
	mode := info.Mode() & (os.ModePerm | os.ModeSticky | os.ModeSetuid | os.ModeSetgid)
	if leaf {
		if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
			return fmt.Errorf("persistent tmp ownership mismatch")
		}
		if mode != os.ModeSticky|0777 {
			return fmt.Errorf("persistent tmp directory permissions are unsafe")
		}
		return nil
	}
	if !trustedOwner(info) {
		return fmt.Errorf("persistent tmp parent must be supervisor-owned")
	}
	if mode != 0700 && mode != 0711 {
		return fmt.Errorf("persistent tmp parent permissions are unsafe")
	}
	// A concurrent creator or restrictive umask may leave a trusted private
	// initialization directory. Complete traversal without repairing unsafe modes.
	if mode == 0700 {
		return file.Chmod(0711)
	}
	return nil
}
