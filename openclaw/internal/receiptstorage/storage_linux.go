//go:build linux

package receiptstorage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const marker = ".clawmanager-receipt-owner.json"

func Ensure(root string, id Identity, create bool, excluded ...string) error {
	if err := Validate(root, id, excluded...); err != nil {
		return err
	}
	for p := filepath.Clean(root); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("receipt mount must exist without symlink ancestors")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("cannot open Pro receipt mount")
	}
	defer dir.Close()
	info, err := dir.Stat(".")
	if err != nil {
		return fmt.Errorf("cannot inspect Pro receipt mount")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0001 == 0 {
		return fmt.Errorf("Pro receipt mount requires root ownership, traversal and no group/other write access")
	}
	before, err := dir.Lstat(marker)
	if os.IsNotExist(err) && create {
		// Claim only an empty dedicated mount. Never adopt another volume's data.
		parent, e := dir.Open(".")
		if e != nil {
			return fmt.Errorf("cannot inspect receipt mount contents")
		}
		entries, e := parent.ReadDir(1)
		parent.Close()
		if e != nil && e != io.EOF {
			return fmt.Errorf("cannot inspect receipt mount contents")
		}
		if len(entries) != 0 {
			return fmt.Errorf("unbound receipt mount is not empty")
		}
		f, e := dir.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			return fmt.Errorf("cannot bind Pro receipt mount")
		}
		_, e = f.Write(binding(id))
		if e == nil {
			e = f.Chmod(0644)
		}
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return fmt.Errorf("cannot persist Pro receipt ownership")
		}
		before, err = dir.Lstat(marker)
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > 4096 {
		return fmt.Errorf("invalid Pro receipt ownership record")
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || before.Mode().Perm() != 0644 {
		return fmt.Errorf("unsafe Pro receipt ownership record")
	}
	f, err := dir.Open(marker)
	if err != nil {
		return fmt.Errorf("cannot read Pro receipt ownership")
	}
	opened, e := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if e != nil || !os.SameFile(before, opened) || readErr != nil || !bytes.Equal(raw, binding(id)) {
		return fmt.Errorf("Pro receipt volume belongs to another identity or is damaged")
	}
	created := false
	if create {
		if err := dir.Mkdir("receipts", 0700); err == nil {
			created = true
		} else if !os.IsExist(err) {
			return fmt.Errorf("cannot create Pro receipt directory")
		}
	}
	before, err = dir.Lstat("receipts")
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe Pro receipt directory")
	}
	child, err := dir.OpenRoot("receipts")
	if err != nil {
		return fmt.Errorf("cannot open Pro receipt directory")
	}
	defer child.Close()
	fd, err := child.Open(".")
	if err != nil {
		return fmt.Errorf("cannot inspect Pro receipt directory")
	}
	defer fd.Close()
	opened, err = fd.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return fmt.Errorf("Pro receipt directory changed")
	}
	if created {
		if err := fd.Chown(id.UID, id.GID); err != nil {
			return fmt.Errorf("cannot assign Pro receipt ownership")
		}
		if err := fd.Chmod(0700); err != nil {
			return fmt.Errorf("cannot restrict Pro receipt directory")
		}
	}
	opened, err = fd.Stat()
	if err != nil {
		return fmt.Errorf("cannot verify Pro receipt directory")
	}
	owner, ok = opened.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(id.UID) || owner.Gid != uint32(id.GID) || opened.Mode().Perm() != 0700 {
		return fmt.Errorf("Pro receipt directory identity or permission mismatch")
	}
	return nil
}
