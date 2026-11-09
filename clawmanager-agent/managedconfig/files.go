//go:build !windows

// Package managedconfig migrates only native-generated credential files before
// any native hook runs. Native adapters own the selected names and formats.
package managedconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type pendingFile struct {
	root *os.Root
	name string
	info os.FileInfo
	data []byte
}

// Rewrite validates and transforms the complete bounded selection before writing
// anything. Existing ownership/mode is retained; no native/user hook is invoked.
func Rewrite(ctx context.Context, base, workspace string, directories []Directory) error {
	admin, err := workspaceio.Open(base)
	if err != nil {
		return ErrUnsafe
	}
	defer admin.Close()
	rel, err := admin.Rel(workspace)
	if err != nil {
		return ErrUnsafe
	}
	instance, err := openDirectory(admin.Root, rel)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrUnsafe
	}
	defer instance.Close()
	var roots []*os.Root
	defer func() {
		for _, r := range roots {
			r.Close()
		}
	}()
	var pending []pendingFile
	entries, total := 0, 0
	for _, directory := range directories {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		root, err := openDirectory(instance, directory.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ErrUnsafe
		}
		roots = append(roots, root)
		dir, err := root.Open(".")
		if err != nil {
			return ErrUnsafe
		}
		names, err := dir.ReadDir(MaxEntries - entries + 1)
		dir.Close()
		if err != nil && err != io.EOF {
			return ErrUnsafe
		}
		entries += len(names)
		if entries > MaxEntries {
			return ErrUnsafe
		}
		for _, entry := range names {
			transform := directory.Select(entry.Name())
			if transform == nil {
				continue
			}
			if len(pending) >= MaxFiles {
				return ErrUnsafe
			}
			data, info, err := readFile(root, entry.Name())
			if err != nil {
				return ErrUnsafe
			}
			total += len(data)
			if total > maxTotalBytes {
				return ErrUnsafe
			}
			changed, err := transform(data)
			if err != nil || len(changed) > MaxFileBytes {
				return ErrUnsafe
			}
			total += len(changed)
			if total > maxTotalBytes {
				return ErrUnsafe
			}
			if bytes.Equal(changed, data) {
				changed = nil
			}
			pending = append(pending, pendingFile{root, entry.Name(), info, changed})
		}
	}
	for _, file := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, current, err := readFile(file.root, file.name)
		if err != nil || !os.SameFile(file.info, current) {
			return ErrUnsafe
		}
		if file.data != nil {
			if err := replace(file); err != nil {
				return ErrUnsafe
			}
		}
	}
	return nil
}
func openDirectory(parent *os.Root, relative string) (*os.Root, error) {
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
		return nil, ErrUnsafe
	}
	root, err := parent.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		if part == ".." {
			root.Close()
			return nil, ErrUnsafe
		}
		before, err := root.Lstat(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, ErrUnsafe
		}
		child, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		after, err := child.Stat(".")
		root.Close()
		if err != nil || !os.SameFile(before, after) {
			child.Close()
			return nil, ErrUnsafe
		}
		root = child
	}
	return root, nil
}
func readFile(root *os.Root, name string) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil || !safeFile(before) {
		return nil, nil, ErrUnsafe
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, ErrUnsafe
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !safeFile(info) || !os.SameFile(before, info) {
		return nil, nil, ErrUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes {
		return nil, nil, ErrUnsafe
	}
	return data, info, nil
}
func safeFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
func replace(p pendingFile) error {
	owner := p.info.Sys().(*syscall.Stat_t)
	return writeAtomic(p.root, p.name, p.data, p.info.Mode().Perm(), int(owner.Uid), int(owner.Gid), p.info)
}

// Create writes a newly generated managed file through the same pinned boundary.
// Existing files must instead be validated by Rewrite.
func Create(ctx context.Context, base, workspace, directory, name string, data []byte, mode os.FileMode, uid, gid int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(data) > MaxFileBytes || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || uid < 0 || gid < 0 {
		return ErrUnsafe
	}
	admin, err := workspaceio.Open(base)
	if err != nil {
		return ErrUnsafe
	}
	defer admin.Close()
	rel, err := admin.Rel(filepath.Join(workspace, directory))
	if err != nil {
		return ErrUnsafe
	}
	root, err := openDirectory(admin.Root, rel)
	if err != nil {
		return ErrUnsafe
	}
	defer root.Close()
	if _, err = root.Lstat(name); !os.IsNotExist(err) {
		return ErrUnsafe
	}
	return writeAtomic(root, name, data, mode, uid, gid, nil)
}
func writeAtomic(root *os.Root, target string, data []byte, mode os.FileMode, uid, gid int, before os.FileInfo) error {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".cm-migration-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	defer root.Remove(name)
	current, err := f.Stat()
	if err != nil {
		return err
	}
	created := current.Sys().(*syscall.Stat_t)
	if int(created.Uid) != uid || int(created.Gid) != gid {
		if err = f.Chown(uid, gid); err != nil {
			return err
		}
	}
	if err = f.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	current, err = root.Lstat(target)
	if before == nil {
		if !os.IsNotExist(err) {
			return ErrUnsafe
		}
	} else if err != nil || !safeFile(current) || !os.SameFile(current, before) {
		return ErrUnsafe
	}
	return root.Rename(name, target)
}

// Subdirectories discovers one native-owned agent namespace, without recursive
// workspace walking. Callers own the fixed parent and append native filenames.
func Subdirectories(ctx context.Context, base, workspace, relative string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	admin, err := workspaceio.Open(base)
	if err != nil {
		return nil, ErrUnsafe
	}
	defer admin.Close()
	rel, err := admin.Rel(filepath.Join(workspace, relative))
	if err != nil {
		return nil, ErrUnsafe
	}
	root, err := openDirectory(admin.Root, rel)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrUnsafe
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		return nil, ErrUnsafe
	}
	defer file.Close()
	entries, err := file.ReadDir(MaxEntries + 1)
	if err != nil && err != io.EOF {
		return nil, ErrUnsafe
	}
	if len(entries) > MaxEntries {
		return nil, ErrUnsafe
	}
	var names []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrUnsafe
		}
		if !entry.IsDir() {
			continue
		}
		if len(names) >= MaxFiles {
			return nil, ErrUnsafe
		}
		child, err := openDirectory(root, entry.Name())
		if err != nil {
			return nil, ErrUnsafe
		}
		child.Close()
		names = append(names, entry.Name())
	}
	return names, nil
}
