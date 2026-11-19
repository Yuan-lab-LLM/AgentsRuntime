//go:build linux

package workspaceio

import (
	"fmt"
	"os"
	"syscall"
)

// ValidateNFSPrivateTree is a read-only admission audit. The caller must hold
// the existing writer/admission gates with the previous untrusted execution
// stopped. Same-instance hardlinks are valid; a path never grants ownership of
// another instance's inode. Team grants are audited separately, not traversed.
func ValidateNFSPrivateTree(path string, uid int, tmp bool) error {
	if err := ValidateInstanceCaller(uid, uid); err != nil {
		return err
	}
	root, err := Open(path)
	if err != nil {
		return err
	}
	defer root.Close()
	return validateNFSPrivateRoot(root.Root, uid, tmp)
}

// ValidateNFSPrivateFD consumes only a pinned grant supplied by management. It
// retains access beneath management0700 ancestors without widening permissions.
func ValidateNFSPrivateFD(file *os.File, uid int, tmp bool) error {
	if err := ValidateInstanceCaller(uid, uid); err != nil {
		return err
	}
	before, err := file.Stat()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return err
	}
	defer root.Close()
	after, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !before.IsDir() || !os.SameFile(before, after) {
		return fmt.Errorf("private audit grant changed")
	}
	return validateNFSPrivateRoot(root, uid, tmp)
}

func validateNFSPrivateRoot(root *os.Root, uid int, tmp bool) error {
	var walk func(*os.Root, bool) error
	walk = func(dir *os.Root, grant bool) error {
		info, err := dir.Stat(".")
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != uint32(uid) && !(grant && st.Uid == NFSManagementUID)) {
			return fmt.Errorf("foreign private directory owner")
		}
		// Grant roots include administrator-provisioned sticky legacy tmp. Their
		// existing grant check controls modes; descendants never gain this exception.
		if !(grant && tmp) && (info.Mode().Perm()&0002 != 0 || info.Mode().Perm()&0020 != 0 && st.Gid != uint32(uid)) {
			return fmt.Errorf("foreign-writable private directory")
		}
		f, err := dir.Open(".")
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			// O_PATH pins metadata without reading contents or blocking on a FIFO.
			file, err := dir.OpenFile(entry.Name(), 0x200000|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			file.Close()
			if err != nil {
				return err
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != uint32(uid) {
				return fmt.Errorf("foreign private inode: %s", entry.Name())
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			} // Never follow aliases during audit.
			if info.Mode().Perm()&0002 != 0 || info.Mode().Perm()&0020 != 0 && st.Gid != uint32(uid) {
				return fmt.Errorf("foreign-writable private inode: %s", entry.Name())
			}
			if info.IsDir() {
				child, err := dir.OpenRoot(entry.Name())
				if err != nil {
					return err
				}
				actual, err := child.Stat(".")
				if err == nil && !os.SameFile(info, actual) {
					err = fmt.Errorf("private directory changed during audit")
				}
				if err == nil {
					err = walk(child, false)
				}
				child.Close()
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, true)
}
