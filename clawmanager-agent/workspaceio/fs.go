// Package workspaceio bounds privileged management I/O to an opened workspace.
package workspaceio

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type FS struct {
	Root *os.Root
	Base string
}

func Open(base string) (*FS, error) {
	base = filepath.Clean(base)
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("workspace root must be absolute")
	}
	before, err := os.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace root must be a real directory")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, fmt.Errorf("workspace root changed during open")
	}
	return &FS{Root: root, Base: base}, nil
}

// OpenAt walks from an administrator-owned root without accepting redirected
// intermediate directories. Each opened inode is verified before it is used.
func OpenAt(base, target string) (*FS, error) {
	root, err := Open(base)
	if err != nil {
		return nil, err
	}
	rel, err := root.Rel(target)
	if err != nil {
		root.Close()
		return nil, err
	}
	if rel == "." {
		return root, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		before, err := root.Root.Lstat(part)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("workspace path requires real directories")
		}
		next, err := root.Root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			root.Close()
			return nil, fmt.Errorf("workspace directory changed during open")
		}
		old := root.Root
		root.Root = next
		root.Base = filepath.Join(root.Base, part)
		old.Close()
	}
	return root, nil
}

func (s *FS) Close() error { return s.Root.Close() }
func (s *FS) Rel(name string) (string, error) {
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(s.Base, name)
		if err != nil {
			return "", err
		}
		name = rel
	}
	name = filepath.Clean(name)
	if name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path escaped instance")
	}
	return name, nil
}
func (s *FS) Open(name string) (*os.File, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return nil, err
	}
	return s.Root.Open(rel)
}
func (s *FS) OpenFile(name string, flag int, mode os.FileMode) (*os.File, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return nil, err
	}
	return s.Root.OpenFile(rel, flag, mode)
}
func (s *FS) Lstat(name string) (os.FileInfo, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return nil, err
	}
	return s.Root.Lstat(rel)
}
func (s *FS) Stat(name string) (os.FileInfo, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return nil, err
	}
	return s.Root.Stat(rel)
}
func (s *FS) ReadFile(name string) ([]byte, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return nil, err
	}
	return s.Root.ReadFile(rel)
}
func (s *FS) Readlink(name string) (string, error) {
	rel, err := s.Rel(name)
	if err != nil {
		return "", err
	}
	return s.Root.Readlink(rel)
}
func (s *FS) ReadDir(name string) ([]os.DirEntry, error) {
	file, err := s.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}
func (s *FS) Mkdir(name string, mode os.FileMode) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	return s.Root.Mkdir(rel, mode)
}
func (s *FS) MkdirAll(name string, mode os.FileMode) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	return s.Root.MkdirAll(rel, mode)
}
func (s *FS) Remove(name string) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	return s.Root.Remove(rel)
}
func (s *FS) RemoveAll(name string) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	return s.Root.RemoveAll(rel)
}
func (s *FS) Symlink(old, new string) error {
	rel, err := s.Rel(new)
	if err != nil {
		return err
	}
	return s.Root.Symlink(old, rel)
}
func (s *FS) Rename(old, new string) error {
	a, err := s.Rel(old)
	if err != nil {
		return err
	}
	b, err := s.Rel(new)
	if err != nil {
		return err
	}
	return s.Root.Rename(a, b)
}
func (s *FS) Chmod(name string, mode os.FileMode) error {
	file, err := s.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Chmod(mode)
}
func (s *FS) Chown(name string, uid, gid int) error {
	if runtime.GOOS == "windows" || uid <= 0 || gid <= 0 {
		return nil
	}
	file, err := s.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Chown(uid, gid)
}
func (s *FS) WriteFile(name string, data []byte, mode os.FileMode) error {
	return s.WriteFileAtomic(name, data, mode)
}
func (s *FS) WriteFileAtomic(name string, data []byte, mode os.FileMode) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(rel), ".cm-"+hex.EncodeToString(nonce[:])+".tmp")
	file, err := s.Root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer s.Root.Remove(temp)
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return s.Root.Rename(temp, rel)
}
func (s *FS) ChownTree(name string, uid, gid int) error {
	rel, err := s.Rel(name)
	if err != nil {
		return err
	}
	return fs.WalkDir(s.Root.FS(), filepath.ToSlash(rel), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return s.Chown(filepath.FromSlash(path), uid, gid)
	})
}

func (s *FS) Child(name string, mode os.FileMode) (*FS, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("invalid workspace directory component")
	}
	if err := s.Root.Mkdir(name, mode.Perm()); err != nil && !os.IsExist(err) {
		return nil, err
	}
	before, err := s.Root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace path must be a real directory")
	}
	root, err := s.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, fmt.Errorf("workspace directory changed during open")
	}
	return &FS{Root: root, Base: filepath.Join(s.Base, name)}, nil
}
func OpenOrCreateAt(base, target string, mode os.FileMode) (*FS, error) {
	current, err := Open(base)
	if err != nil {
		return nil, err
	}
	rel, err := current.Rel(target)
	if err != nil {
		current.Close()
		return nil, err
	}
	if rel == "." {
		return current, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next, err := current.Child(part, mode)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}
func (s *FS) Chgrp(name string, gid int) error {
	if runtime.GOOS == "windows" || gid <= 0 {
		return nil
	}
	file, err := s.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Chown(-1, gid)
}

func CreateAt(base, target string, mode os.FileMode) (*FS, error) {
	return OpenOrCreateAt(base, target, mode)
}

// Ensure creates a missing instance tree from its nearest existing real parent.
// Production managers prefer CreateAt from their administrator-owned pool root.
func Ensure(base string, mode os.FileMode) (*FS, error) {
	base = filepath.Clean(base)
	ancestor := base
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, err
		}
		ancestor = parent
	}
	return CreateAt(ancestor, base, mode)
}

// PrepareManagementRoots protects only fixed supervisor-owned directories.
// Existing user-owned parents require explicit offline administrator repair.
func PrepareManagementRoots(base string, children ...string) error {
	root, err := Ensure(base, 0755)
	if err != nil {
		return err
	}
	defer root.Close()
	protect := func(current *FS) error {
		file, err := current.Open(".")
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !ownedBySupervisor(info) {
			return fmt.Errorf("management directory must already be supervisor-owned")
		}
		return file.Chmod(0755)
	}
	if err := protect(root); err != nil {
		return err
	}
	for _, name := range children {
		child, err := root.Child(name, 0755)
		if err != nil {
			return err
		}
		err = protect(child)
		child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// AllowManagementTraversal grants search permission through fixed trusted
// parents so an already unprivileged sandbox launcher can bind a narrow child.
// Parents stay supervisor-owned and unlistable/unwritable to other users. The
// sandbox must never mount these management ancestors themselves.
func AllowManagementTraversal(base, target string) error {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("management traversal requires an exact descendant")
	}
	current, err := Open(base)
	if err != nil {
		return err
	}
	defer func() { current.Close() }()
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		child, err := current.Child(component, 0700)
		if err != nil {
			return err
		}
		current.Close()
		current = child
		file, err := current.Open(".")
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if !ownedBySupervisor(info) {
			file.Close()
			return fmt.Errorf("management traversal parent must be supervisor-owned")
		}
		err = file.Chmod(0711)
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
