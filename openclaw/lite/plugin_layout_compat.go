package openclaw

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// prepareNativePluginLayout performs the existing project-to-global rollback
// compatibility step inside the assigned instance, under its own credentials.
// Existing global packages remain authoritative; no ownership repair is needed.
func prepareNativePluginLayout(workspace string) error {
	root, err := workspaceio.Open(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	npm := filepath.Join("home", ".openclaw", "npm")
	projects, err := root.ReadDir(filepath.Join(npm, "projects"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	global := filepath.Join(npm, "node_modules")
	info, err := root.Lstat(global)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("OpenClaw global node_modules must be a directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	candidates := map[string][]string{}
	inspect := func(name, path string) error {
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		manifest, err := root.Stat(filepath.Join(path, "openclaw.plugin.json"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !manifest.IsDir() {
			candidates[name] = append(candidates[name], path)
		}
		return nil
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		modules := filepath.Join(npm, "projects", project.Name(), "node_modules")
		entries, err := root.ReadDir(modules)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			path := filepath.Join(modules, name)
			if strings.HasPrefix(name, "@") && entry.IsDir() {
				scoped, err := root.ReadDir(path)
				if err != nil {
					return err
				}
				for _, member := range scoped {
					if !strings.HasPrefix(member.Name(), ".") {
						if err = inspect(name+"/"+member.Name(), filepath.Join(path, member.Name())); err != nil {
							return err
						}
					}
				}
			} else if err = inspect(name, path); err != nil {
				return err
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if err = root.MkdirAll(global, 0755); err != nil {
		return err
	}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		link := filepath.Join(global, filepath.FromSlash(name))
		if _, err = root.Lstat(link); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		targets := candidates[name]
		if len(targets) != 1 {
			return fmt.Errorf("OpenClaw plugin %q exists in multiple npm projects", name)
		}
		parent := filepath.Dir(link)
		if parent != global {
			if info, err = root.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
				continue
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err = root.MkdirAll(parent, 0755); err != nil {
				return err
			}
		}
		target, err := filepath.Rel(parent, targets[0])
		if err != nil {
			return err
		}
		if err = root.Symlink(target, link); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}
