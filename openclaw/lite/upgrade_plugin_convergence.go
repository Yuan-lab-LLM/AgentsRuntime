package openclaw

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A plan describes only image-managed payload, never credentials or user files.
// Both rehearsal and the stopped-writer migration derive the same fingerprint.
type managedPluginChange struct {
	Project            string `json:"project"`
	ImageProject       string `json:"image_project"`
	SourceImageProject string `json:"source_image_project"`
	Name               string `json:"name"`
	Version            string `json:"version"`
	PreviousVersion    string `json:"previous_version"`
	OriginalMetadata   string `json:"original_metadata"`
	TargetMetadata     string `json:"target_metadata"`
}
type managedPluginPlan struct {
	Changes []managedPluginChange `json:"changes"`
	SHA256  string                `json:"sha256"`
}

func (m *upgradeService) pluginProjectMetadata(project string) (string, string, string, error) {
	raw, err := m.io.ReadFile(filepath.Join(project, "package.json"))
	if err != nil {
		return "", "", "", err
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
		Private      *bool             `json:"private"`
		Overrides    map[string]string `json:"overrides"`
		OpenClaw     struct {
			ManagedOverrides []string `json:"managedOverrides"`
		} `json:"openclaw"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", "", "", fmt.Errorf("invalid plugin project metadata")
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		return "", "", "", err
	}
	for key := range fields {
		if key != "dependencies" && key != "private" && key != "overrides" && key != "openclaw" {
			return "", "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: custom project metadata %s", key)
		}
	}
	if pkg.Private != nil && !*pkg.Private {
		return "", "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: non-private managed project")
	}
	managed := map[string]bool{}
	for _, name := range pkg.OpenClaw.ManagedOverrides {
		managed[name] = true
	}
	if len(managed) != len(pkg.Overrides) {
		return "", "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: custom dependency overrides")
	}
	for key := range pkg.Overrides {
		if !managed[key] {
			return "", "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: custom dependency override %s", key)
		}
	}
	if data := fields["openclaw"]; data != nil {
		var keys map[string]json.RawMessage
		if json.Unmarshal(data, &keys) != nil || len(keys) != 1 || keys["managedOverrides"] == nil {
			return "", "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: custom OpenClaw project metadata")
		}
	}
	if err = json.Unmarshal(raw, &pkg); err != nil || len(pkg.Dependencies) != 1 {
		return "", "", "", fmt.Errorf("managed plugin project has ambiguous package identity: %s", filepath.Base(project))
	}
	var name, version string
	for name, version = range pkg.Dependencies {
	}
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(version, " ^~*<>=|") {
		return "", "", "", fmt.Errorf("custom plugin version requires review: %s", name)
	}
	lock, err := m.io.ReadFile(filepath.Join(project, "package-lock.json"))
	if err != nil {
		return "", "", "", err
	}
	var manifest struct {
		Packages map[string]struct {
			Dependencies map[string]string `json:"dependencies"`
		} `json:"packages"`
	}
	if json.Unmarshal(lock, &manifest) != nil || manifest.Packages[""].Dependencies[name] != version {
		return "", "", "", fmt.Errorf("plugin manifest and lock disagree: %s", name)
	}
	h := sha256.New()
	h.Write(raw)
	h.Write([]byte{0})
	h.Write(lock)
	return name, version, hex.EncodeToString(h.Sum(nil)), nil
}

func (m *upgradeService) planManagedPlugins(state, defaults string) (managedPluginPlan, error) {
	plan := managedPluginPlan{Changes: []managedPluginChange{}}
	images := map[string]managedPluginChange{}
	entries, err := m.io.ReadDir(filepath.Join(defaults, "npm/projects"))
	if err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, version, hash, err := m.pluginProjectMetadata(filepath.Join(defaults, "npm/projects", e.Name()))
		if err != nil {
			continue
		}
		if _, ok := images[name]; ok {
			return plan, fmt.Errorf("ambiguous image plugin: %s", name)
		}
		images[name] = managedPluginChange{ImageProject: e.Name(), Name: name, Version: version, TargetMetadata: hash}
	}
	entries, err = m.io.ReadDir(filepath.Join(state, "npm/projects"))
	if err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		project := filepath.Join(state, "npm/projects", e.Name())
		// An image-linked package identity is the legacy ownership marker. An
		// actual custom package is never replaced merely because names match.
		owned := false
		err = m.io.WalkDir(filepath.Join(project, "node_modules"), func(path string, d fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink == 0 {
				return nil
			}
			link, err := m.io.Readlink(path)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(path), link)
			}
			if pathWithin(filepath.Join(defaults, "npm/projects"), filepath.Clean(link)) {
				owned = true
			}
			return nil
		})
		if err != nil {
			return plan, err
		}
		if !owned {
			continue
		}
		name, version, hash, err := m.pluginProjectMetadata(project)
		if err != nil {
			return plan, err
		}
		fresh, ok := images[name]
		if !ok {
			return plan, fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: no image replacement for %s", name)
		}
		// Do not overwrite a real package override in a mixed managed project.
		pkg := filepath.Join(project, "node_modules", filepath.FromSlash(name))
		link, base, err := m.managedPackageImageTarget(pkg, project, filepath.Join(defaults, "npm/projects"))
		if err != nil {
			return plan, err
		}
		rel, err := filepath.Rel(base, filepath.Clean(link))
		if err != nil || strings.HasPrefix(rel, "..") || !strings.HasSuffix(filepath.ToSlash(rel), "/node_modules/"+name) {
			return plan, fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: ambiguous image package identity: %s", name)
		}
		fresh.SourceImageProject = strings.Split(filepath.ToSlash(rel), "/")[0]
		broken := false
		_ = m.io.WalkDir(filepath.Join(project, "node_modules"), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.Type()&os.ModeSymlink != 0 {
				if _, e := m.resolveUpgradePluginPath(path, false, state, defaults, "/usr/local/lib/node_modules/openclaw"); e != nil {
					broken = true
				}
			}
			return nil
		})
		if hash != fresh.TargetMetadata || version != fresh.Version || broken || fresh.SourceImageProject != fresh.ImageProject {
			fresh.Project = e.Name()
			fresh.PreviousVersion = version
			fresh.OriginalMetadata = hash
			plan.Changes = append(plan.Changes, fresh)
		}
	}
	sort.Slice(plan.Changes, func(i, j int) bool { return plan.Changes[i].Project < plan.Changes[j].Project })
	raw, _ := json.Marshal(plan.Changes)
	hash := sha256.Sum256(raw)
	plan.SHA256 = hex.EncodeToString(hash[:])
	return plan, nil
}

func (m *upgradeService) managedPackageImageTarget(pkg, project, base string) (string, string, error) {
	if real, err := m.resolveUpgradePluginPath(pkg, false, project, base, "/usr/local/lib/node_modules/openclaw"); err == nil {
		root, e := m.resolveUpgradePluginPath(base, false, base, "/usr/local/lib/node_modules/openclaw")
		if e == nil && pathWithin(root, real) {
			return real, root, nil
		}
	}
	// Scope directories may themselves be image links. Preserve this ownership
	// across rebuilds without mistaking the package below @scope for a custom copy.
	for leaf := pkg; pathWithin(project, leaf) && leaf != project; leaf = filepath.Dir(leaf) {
		if link, err := m.io.Readlink(leaf); err == nil {
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(leaf), link)
			}
			rel, _ := filepath.Rel(leaf, pkg)
			target := filepath.Clean(filepath.Join(link, rel))
			if pathWithin(base, target) {
				return target, base, nil
			}
		}
	}
	return "", "", fmt.Errorf("OPENCLAW_PLUGIN_CUSTOM_REVIEW_REQUIRED: user-owned or ambiguous package override %s", filepath.Base(pkg))
}

func (m *upgradeService) applyManagedPluginPlan(state, defaults string, plan managedPluginPlan) error {
	for _, change := range plan.Changes {
		project := filepath.Join(state, "npm/projects", change.Project)
		image := filepath.Join(defaults, "npm/projects", change.ImageProject)
		_, _, hash, err := m.pluginProjectMetadata(project)
		if err != nil || hash != change.OriginalMetadata {
			return fmt.Errorf("managed plugin inputs changed: %s", change.Name)
		}
		_, _, targetHash, err := m.pluginProjectMetadata(image)
		if err != nil || targetHash != change.TargetMetadata {
			return fmt.Errorf("managed image plugin changed: %s", change.Name)
		}
		// Remove ONLY image-owned links in the isolated candidate. Custom regular
		// files and custom/external links remain and must pass subsequent validation.
		err = m.io.WalkDir(filepath.Join(project, "node_modules"), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink == 0 {
				return nil
			}
			link, err := m.io.Readlink(path)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(path), link)
			}
			if pathWithin(filepath.Join(defaults, "npm/projects", change.SourceImageProject), filepath.Clean(link)) || pathWithin(image, filepath.Clean(link)) {
				return m.io.Remove(path)
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Metadata is known to be machine-managed by the verified plan. Sync the
		// complete version/lock pair; merging links alone would leave a split graph.
		for _, name := range []string{"package.json", "package-lock.json"} {
			raw, err := m.io.ReadFile(filepath.Join(image, name))
			if err != nil {
				return err
			}
			if err = m.io.AtomicWriteFile(filepath.Join(project, name), raw, 0600); err != nil {
				return err
			}
		}
		if err = m.seedImagePluginTreeWithin(filepath.Join(image, "node_modules"), filepath.Join(project, "node_modules"), defaults); err != nil {
			return err
		}
		// Legacy root aliases can still point into an old managed project even
		// after its instance-owned project has converged. Remap only this proven
		// image prefix; unrelated custom links remain untouched and validated.
		for _, root := range []string{"npm/node_modules", "extensions", "plugin-skills"} {
			err = m.io.WalkDir(filepath.Join(state, filepath.FromSlash(root)), func(name string, d fs.DirEntry, walkErr error) error {
				if os.IsNotExist(walkErr) {
					return nil
				}
				if walkErr != nil {
					return walkErr
				}
				if d.Type()&os.ModeSymlink == 0 {
					return nil
				}
				link, err := m.io.Readlink(name)
				if err != nil {
					return err
				}
				if !filepath.IsAbs(link) {
					link = filepath.Join(filepath.Dir(name), link)
				}
				oldImage := filepath.Join(defaults, "npm/projects", change.SourceImageProject)
				if !pathWithin(oldImage, filepath.Clean(link)) {
					return nil
				}
				rel, _ := filepath.Rel(oldImage, link)
				target := filepath.Join(image, rel)
				if _, err = m.io.Stat(target); err != nil {
					return fmt.Errorf("managed plugin alias has no replacement: %s: %w", filepath.Base(name), err)
				}
				if filepath.Clean(link) == filepath.Clean(target) {
					return nil
				}
				if err = m.io.Remove(name); err != nil {
					return err
				}
				return m.io.Symlink(target, name)
			})
			if err != nil {
				return err
			}
		}
	}
	if len(plan.Changes) > 0 {
		raw, _ := json.Marshal(plan)
		return m.io.AtomicWriteFile(filepath.Join(state, ".clawmanager-plugin-convergence.json"), raw, 0600)
	}
	return nil
}
