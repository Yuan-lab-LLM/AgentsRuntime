//go:build linux

package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func prepareReceiptDirectory(root, name string, uid, gid int) error {
	// The mount root is provisioned by the operator, not created in a workspace.
	for p := filepath.Clean(root); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("receipt root must exist without symlink ancestors")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	parent, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("cannot open receipt root")
	}
	defer parent.Close()
	info, err := parent.Stat(".")
	if err != nil {
		return fmt.Errorf("cannot inspect receipt root")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0001 == 0 {
		return fmt.Errorf("receipt root requires control-agent ownership, traversal and no group/other write access")
	}
	created := false
	if err := parent.Mkdir(name, 0700); err == nil {
		created = true
	} else if !os.IsExist(err) {
		return fmt.Errorf("cannot create instance receipt directory")
	}
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe instance receipt directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("cannot open instance receipt directory")
	}
	defer child.Close()
	fd, err := child.Open(".")
	if err != nil {
		return fmt.Errorf("cannot inspect instance receipt directory")
	}
	defer fd.Close()
	current, err := fd.Stat()
	if err != nil || !os.SameFile(before, current) {
		return fmt.Errorf("instance receipt directory changed")
	}
	if created {
		if err := fd.Chown(uid, gid); err != nil {
			return fmt.Errorf("cannot assign receipt directory ownership")
		}
		if err := fd.Chmod(0700); err != nil {
			return fmt.Errorf("cannot restrict receipt directory permissions")
		}
	}
	current, err = fd.Stat()
	if err != nil {
		return fmt.Errorf("cannot verify receipt directory")
	}
	owner, ok := current.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(uid) || owner.Gid != uint32(gid) || current.Mode().Perm() != 0700 {
		return fmt.Errorf("receipt directory ownership or permissions mismatch; existing data left unchanged")
	}
	return nil
}
