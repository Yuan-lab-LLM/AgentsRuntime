package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The lease contains only exact peer addresses, not credentials. It must be
// readable by isolated Gateway UIDs, but writable only by the Agent. Do not
// relax the Agent's global umask or private workspace permissions to achieve it.
func ensureProxyPolicyDirectory(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe proxy policy directory")
		}
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return err
	}
	fd, err := syscall.Open(abs, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("unsafe proxy policy directory")
	}
	defer syscall.Close(fd)
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil || info.Uid != uint32(os.Geteuid()) || info.Mode&0022 != 0 {
		return fmt.Errorf("unsafe proxy policy directory")
	}
	// MkdirAll is affected by umask077. Explicitly set only this opened,
	// Agent-owned directory, including a pre-existing0700 directory. Fchmod
	// cannot follow a swapped leaf symlink or broaden an unowned directory.
	if err := syscall.Fchmod(fd, 0755); err != nil {
		return err
	}
	return nil
}
