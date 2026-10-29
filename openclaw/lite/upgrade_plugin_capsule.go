package openclaw

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func upgradePluginControlRoot(root string) bool {
	return root == "npm" || root == "extensions" || root == "plugin-skills"
}

func upgradeStateCapsuleExcludedRootForVersion(root string, version int) bool {
	if version >= 3 && upgradePluginControlRoot(root) {
		return false
	}
	return upgradeStateCapsuleExcludedRoot(root)
}

// Doctor can relink or replace managed plugin payloads and their metadata.
// Preserve their actual instance-owned files and link entries without copying
// immutable image packages, following links, or capturing user workspaces.
func (m *upgradeService) copyUpgradePluginTree(source, target, stateRoot string) (int64, int64, error) {
	var total, count int64
	err := m.io.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		info, err := m.io.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := m.io.Readlink(name)
			if err != nil {
				return err
			}
			_, err = m.upgradePluginLink(name, stateRoot)
			if err != nil {
				return fmt.Errorf("plugin capsule cannot verify link %s: %w", rel, err)
			}
			if err := m.io.Symlink(link, dest); err != nil {
				if previous, readErr := m.io.Readlink(dest); readErr != nil || previous != link {
					return err
				}
			}
			count++
			return nil
		}
		if info.IsDir() {
			return m.io.MkdirAll(dest, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin capsule refuses special file %s", rel)
		}
		if info.Size() > 1<<30-total {
			return fmt.Errorf("plugin rollback capsule exceeds 1 GiB safety limit")
		}
		if err := m.copyRegularFileStable(name, dest, info.Mode().Perm()); err != nil {
			return err
		}
		total += info.Size()
		count++
		return nil
	})
	return total, count, err
}
