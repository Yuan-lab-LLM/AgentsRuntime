package openclaw

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A rollback archive records a link, not its referent. In particular a legacy
// image-owned dependency may no longer exist in the target image. Permit only
// verified managed/image roots; never follow an unknown external link.
func (m *upgradeService) upgradePluginLink(name, stateRoot string) (string, error) {
	link, err := m.io.Readlink(name)
	if err != nil {
		return "", err
	}
	lexical := link
	if !filepath.IsAbs(lexical) {
		lexical = filepath.Dir(name) + string(filepath.Separator) + lexical
	}
	return m.resolveUpgradePluginPath(lexical, true, stateRoot, "/defaults/.openclaw", "/usr/local/lib/node_modules/openclaw")
}

// Resolve one component at a time through pinned roots. A package symlink may
// cross from this instance into an explicitly approved immutable image tree;
// ordinary Root.Stat cannot follow that boundary, even for a relative .bin link.
// This only verifies a link: callers preserve its text, never read its referent.
func (m *upgradeService) resolveUpgradePluginPath(path string, allowMissingImage bool, roots ...string) (string, error) {
	for hops := 0; hops < 40; hops++ {
		approved := false
		for _, root := range roots {
			if pathWithin(root, path) {
				approved = true
				break
			}
		}
		if !approved {
			return "", fmt.Errorf("plugin link leaves managed roots: %s", path)
		}
		bound, err := m.io.scope(path, false)
		if err != nil {
			return "", err
		}
		prefix := bound.Base + string(filepath.Separator)
		if path != bound.Base && !strings.HasPrefix(path, prefix) {
			return "", fmt.Errorf("plugin link traversal leaves pinned root")
		}
		rel := strings.TrimPrefix(path, prefix)
		if path == bound.Base {
			rel = "."
		}
		parts := strings.Split(rel, string(filepath.Separator))
		current := bound.Base
		followed := false
		for i, part := range parts {
			if part == "" || part == "." {
				continue
			}
			if part == ".." {
				current = filepath.Dir(current)
				if !pathWithin(bound.Base, current) {
					return "", fmt.Errorf("plugin link traversal leaves pinned root")
				}
				continue
			}
			current = filepath.Join(current, part)
			info, err := bound.Lstat(current)
			if err != nil {
				// Old immutable packages may be absent in a newer image. Preserve their
				// dangling link only after its existing image ancestry has been verified.
				if allowMissingImage && errors.Is(err, os.ErrNotExist) {
					for _, root := range roots[1:] {
						if pathWithin(root, current) && pathWithin(root, path) {
							return filepath.Clean(path), nil
						}
					}
				}
				return "", err
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := bound.Readlink(current)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Dir(current) + string(filepath.Separator) + target
			}
			path = target
			if i+1 < len(parts) {
				path += string(filepath.Separator) + strings.Join(parts[i+1:], string(filepath.Separator))
			}
			followed = true
			break
		}
		if !followed {
			return current, nil
		}
	}
	return "", fmt.Errorf("plugin link exceeds symlink traversal limit")
}

func imageOwnedPluginPath(path string) bool {
	return pathWithin("/defaults/.openclaw", path) || pathWithin("/usr/local/lib/node_modules/openclaw", path)
}

// Rebuild image-owned dependency links in an uninstalled candidate/probe.
// Real custom files and non-image plugins are never replaced. The complete
// original is retained by the rebuild journal before the candidate becomes live.
func (m *upgradeService) rebuildImageManagedPlugins(state, defaults string) error {
	plan, err := m.planManagedPlugins(state, defaults)
	if err != nil {
		return err
	}
	if err := m.applyManagedPluginPlan(state, defaults, plan); err != nil {
		return err
	}
	for _, group := range []string{"npm/projects", "extensions"} {
		root := filepath.Join(state, filepath.FromSlash(group))
		entries, err := m.io.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			project := filepath.Join(root, entry.Name())
			image := filepath.Join(defaults, filepath.FromSlash(group), entry.Name())
			info, err := m.io.Lstat(image)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			imageLinks := 0
			err = m.io.WalkDir(project, func(name string, e fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if e.IsDir() {
					return nil
				}
				if e.Type()&os.ModeSymlink == 0 {
					return nil
				}
				link, err := m.io.Readlink(name)
				if err != nil {
					return err
				}
				if !filepath.IsAbs(link) {
					link = filepath.Join(filepath.Dir(name), link)
				}
				if pathWithin(image, filepath.Clean(link)) {
					imageLinks++
					return m.io.Remove(name)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if imageLinks == 0 {
				continue
			} // fully custom installation, not an image clone
			if err := m.seedImagePluginTree(image, project); err != nil {
				return err
			}
		}
	}
	return m.validateUpgradePluginLinks(state)
}

func (m *upgradeService) seedImagePluginTree(image, dest string) error {
	return m.seedImagePluginTreeWithin(image, dest, "")
}

func (m *upgradeService) seedImagePluginTreeWithin(image, dest, approvedRoot string) error {
	if approvedRoot != "" {
		real, err := m.resolveUpgradePluginPath(approvedRoot, false, approvedRoot, "/defaults/.openclaw", "/usr/local/lib/node_modules/openclaw")
		if err != nil {
			return err
		}
		approvedRoot = real
	}
	imageRoots := []string{"/defaults/.openclaw", "/usr/local/lib/node_modules/openclaw"}
	if approvedRoot != "" {
		imageRoots = append(imageRoots, approvedRoot)
	}
	approved := func(path string) bool {
		return imageOwnedPluginPath(path) || (approvedRoot != "" && pathWithin(approvedRoot, path))
	}
	return m.io.WalkDir(image, func(name string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(image, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if old, err := m.io.Lstat(target); err == nil {
			if e.IsDir() {
				if old.Mode()&os.ModeSymlink != 0 {
					return filepath.SkipDir
				}
				if !old.IsDir() {
					return fmt.Errorf("custom plugin file conflicts with image directory: %s", target)
				}
				// A dependency directory emptied of old image links can use one
				// immutable tree link, rather than copying thousands of NFS files.
				if target != dest {
					children, err := m.io.ReadDir(target)
					if err != nil {
						return err
					}
					if len(children) == 0 {
						if err := m.io.Remove(target); err != nil {
							return err
						}
						resolved, err := m.resolveUpgradePluginPath(name, false, imageRoots...)
						if err != nil {
							return err
						}
						if !approved(resolved) {
							return ErrWorkspacePath
						}
						if err := m.io.Symlink(resolved, target); err != nil {
							return err
						}
						return filepath.SkipDir
					}
				}
				return nil // merge children of an existing nonempty directory
			} else {
				return nil
			} // user-owned override, still checked by core validation
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if e.IsDir() {
			if target != dest {
				resolved, err := m.resolveUpgradePluginPath(name, false, imageRoots...)
				if err != nil {
					return err
				}
				if !approved(resolved) {
					return ErrWorkspacePath
				}
				if err := m.io.Symlink(resolved, target); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return m.io.MkdirAll(target, 0755)
		}
		resolved, err := m.resolveUpgradePluginPath(name, false, imageRoots...)
		if err != nil {
			return err
		}
		if !approved(resolved) {
			return fmt.Errorf("image plugin payload leaves immutable roots: %s", name)
		}
		return m.io.Symlink(resolved, target)
	})
}

func (m *upgradeService) validateUpgradePluginLinks(state string) error {
	for _, root := range []string{"npm", "extensions", "plugin-skills"} {
		base := filepath.Join(state, root)
		if _, err := m.io.Lstat(base); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := m.io.WalkDir(base, func(name string, e fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e.Type()&os.ModeSymlink == 0 {
				return nil
			}
			resolved, err := m.resolveUpgradePluginPath(name, false, state, "/defaults/.openclaw", "/usr/local/lib/node_modules/openclaw")
			if err != nil {
				return fmt.Errorf("OPENCLAW_PLUGIN_REBUILD_REQUIRED: plugin dependency is unavailable at %s; retain data and rebuild using a compatible image: %w", strings.TrimPrefix(name, state+string(filepath.Separator)), err)
			}
			if !pathWithin(state, resolved) && !imageOwnedPluginPath(resolved) {
				return fmt.Errorf("plugin dependency leaves managed roots: %s", name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
