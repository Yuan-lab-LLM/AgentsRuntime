package openclaw

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Preserve the capsule for retries. The only publish is a rename within the
// destination mount; a crash while copying leaves the live target absent.
func (m *upgradeService) restoreUpgradeDirectory(original, target string) error {
	if !m.scopedNative {
		return m.io.Rename(original, target)
	}
	staged, err := m.io.MkdirTemp(filepath.Dir(target), ".clawmanager-restore-")
	if err != nil {
		return err
	}
	defer m.io.RemoveAll(staged)
	var total int64
	err = m.io.WalkDir(original, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(original, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(staged, rel)
		info, err := m.io.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := m.io.Readlink(path)
			if err != nil {
				return err
			}
			return m.io.Symlink(link, destination) // Copy the link entry without following it.
		}
		if info.IsDir() {
			return m.io.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rollback capsule contains a special file")
		}
		if info.Size() > 1<<30-total {
			return fmt.Errorf("rollback directory exceeds 1 GiB safety limit")
		}
		total += info.Size()
		return m.copyRegularFileStable(path, destination, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	root, err := m.io.Open(staged)
	if err != nil {
		return err
	}
	info, err := m.io.Stat(original)
	if err == nil {
		err = root.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = root.Sync()
	}
	closeErr := root.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return m.io.Rename(staged, target)
}
