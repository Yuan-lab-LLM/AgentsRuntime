package slots

import (
	"fmt"
	"os"
	"path/filepath"
)

func trustedDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return fmt.Errorf("management path contains a symlink")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 || !trustedOwner(info) {
		return fmt.Errorf("management directory must be supervisor-owned and not writable by others")
	}
	return nil
}
func trustedFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !trustedOwner(info) {
		return fmt.Errorf("management file must be regular and supervisor-owned")
	}
	return nil
}

func ensureInstanceParents(root, runtimeType string, userID int) error {
	if runtimeType == "" || filepath.Base(runtimeType) != runtimeType || runtimeType == "." || runtimeType == ".." || userID < 1 {
		return fmt.Errorf("invalid instance parent identity")
	}
	if err := trustedDirectory(root); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(root, runtimeType), filepath.Join(root, runtimeType, fmt.Sprintf("user-%d", userID))} {
		if err := os.Mkdir(path, 0711); err != nil && !os.IsExist(err) {
			return err
		}
		if err := trustedDirectory(path); err != nil {
			return err
		}
	}
	return nil
}
