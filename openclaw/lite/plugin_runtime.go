package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const openClawDefaultsDirEnv = "CLAWMANAGER_OPENCLAW_DEFAULTS_DIR"
const openClawNPMRuntimeModeEnv = "CLAWMANAGER_OPENCLAW_NPM_RUNTIME_MODE"
const defaultOpenClawDefaultsDir = "/defaults/.openclaw"
const defaultOpenClawGlobalPackageDir = "/usr/local/lib/node_modules/openclaw"
const openClawNPMRuntimeModeShared = "shared"
const openClawNPMRuntimeModeCopy = "copy"

var openClawPluginRuntimeDirs = []string{"plugins", "extensions"}
var openClawGlobalPackageDir = defaultOpenClawGlobalPackageDir

type openClawInstancePaths struct {
	Workspace         string
	Home              string
	Persistent        string
	OpenClawWorkspace string
}

func resolveOpenClawInstancePaths(req gateway.CreateGatewayRequest, workspacePath string) (openClawInstancePaths, error) {
	paths := openClawInstancePaths{
		Workspace:  filepath.Clean(workspacePath),
		Home:       filepath.Join(workspacePath, "home"),
		Persistent: filepath.Join(workspacePath, "home", ".openclaw"),
	}
	paths.OpenClawWorkspace = filepath.Join(paths.Persistent, "workspace")

	checks := []struct {
		name string
		want string
	}{
		{name: "CLAWMANAGER_WORKSPACE_PATH", want: paths.Workspace},
		{name: "HOME", want: paths.Home},
		{name: "CLAWMANAGER_AGENT_PERSISTENT_DIR", want: paths.Persistent},
	}
	for _, check := range checks {
		value, ok := requestEnvValue(req, check.name)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if filepath.Clean(value) != filepath.Clean(check.want) {
			return openClawInstancePaths{}, fmt.Errorf("%s does not match validated Lite workspace", check.name)
		}
	}
	return paths, nil
}

func (s *openClawIO) seedOpenClawPluginRuntime(req gateway.CreateGatewayRequest, activeRoot string) error {
	defaultsRoot := strings.TrimSpace(os.Getenv(openClawDefaultsDirEnv))
	if defaultsRoot == "" {
		defaultsRoot = defaultOpenClawDefaultsDir
	}
	return s.seedOpenClawPluginRuntimeFrom(defaultsRoot, req, activeRoot)
}

func (s *openClawIO) seedOpenClawPluginRuntimeFrom(defaultsRoot string, req gateway.CreateGatewayRequest, activeRoot string) error {
	defaultsRoot = filepath.Clean(defaultsRoot)
	info, err := os.Stat(defaultsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return s.ensureDingTalkOpenClawPeer(activeRoot)
		}
		return fmt.Errorf("stat OpenClaw defaults: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("OpenClaw defaults path is not a directory: %s", defaultsRoot)
	}
	if err := ensureOpenClawDefaultsTraversal(defaultsRoot); err != nil {
		return fmt.Errorf("prepare OpenClaw defaults traversal: %w", err)
	}

	mode, err := resolveOpenClawNPMRuntimeMode()
	if err != nil {
		return err
	}
	if mode == openClawNPMRuntimeModeShared {
		image, err := openOpenClawIO(defaultsRoot, "")
		if err != nil {
			return err
		}
		defer image.Close()
		if err := image.ensureDingTalkOpenClawPeer(defaultsRoot); err != nil {
			return fmt.Errorf("prepare shared OpenClaw peer: %w", err)
		}
	}
	npmSource := filepath.Join(defaultsRoot, "npm")
	npmTarget := filepath.Join(activeRoot, "npm")
	var npmSeeded bool
	if mode == openClawNPMRuntimeModeCopy {
		npmSeeded, err = s.copyOpenClawPluginDirIfMissing(npmSource, npmTarget)
	} else {
		npmSeeded, err = s.seedSharedOpenClawNPMIfMissing(npmSource, npmTarget)
	}
	if err != nil {
		return fmt.Errorf("seed OpenClaw plugin runtime npm (%s): %w", mode, err)
	}
	if npmSeeded {
		if err := s.chownTree(npmTarget, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown OpenClaw plugin runtime npm: %w", err)
		}
	}

	for _, name := range openClawPluginRuntimeDirs {
		source := filepath.Join(defaultsRoot, name)
		target := filepath.Join(activeRoot, name)
		copied, err := s.copyOpenClawPluginDirIfMissing(source, target)
		if err != nil {
			return fmt.Errorf("seed OpenClaw plugin runtime %s: %w", name, err)
		}
		if copied {
			if err := s.chownTree(target, req.UID, req.GID); err != nil {
				return fmt.Errorf("chown OpenClaw plugin runtime %s: %w", name, err)
			}
		}
	}
	if err := s.ensureDingTalkOpenClawPeer(activeRoot); err != nil {
		return err
	}

	registryPath := filepath.Join(activeRoot, "plugins", "installs.json")
	if err := s.rewriteOpenClawPluginRegistry(registryPath, defaultsRoot, activeRoot); err != nil {
		return err
	}
	if _, err := s.Stat(registryPath); err == nil {
		if err := s.Chown(registryPath, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown OpenClaw plugin registry: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat OpenClaw plugin registry: %w", err)
	}
	return nil
}

func ensureOpenClawDefaultsTraversal(defaultsRoot string) error {
	for _, dir := range []string{
		filepath.Dir(defaultsRoot),
		defaultsRoot,
		filepath.Join(defaultsRoot, "npm"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.IsDir() {
			continue
		}
		mode := info.Mode().Perm()
		readableMode := mode | 0o055
		if readableMode != mode {
			if err := os.Chmod(dir, readableMode); err != nil {
				return fmt.Errorf("chmod %s: %w", dir, err)
			}
		}
	}
	return nil
}

func resolveOpenClawNPMRuntimeMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(openClawNPMRuntimeModeEnv)))
	if mode == "" {
		return openClawNPMRuntimeModeShared, nil
	}
	switch mode {
	case openClawNPMRuntimeModeShared, openClawNPMRuntimeModeCopy:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid %s %q: want %q or %q", openClawNPMRuntimeModeEnv, mode, openClawNPMRuntimeModeShared, openClawNPMRuntimeModeCopy)
	}
}

// seedSharedOpenClawNPMIfMissing creates a small, instance-owned npm root and
// links each image-provided package into it. The writable package parents let
// an instance replace a default package or install additional packages without
// copying the image's complete node_modules tree during gateway startup.
func (s *openClawIO) seedSharedOpenClawNPMIfMissing(source, target string) (bool, error) {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("source is not a directory: %s", source)
	}
	if _, err := s.Lstat(target); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	parent := filepath.Dir(target)
	if err := s.MkdirAll(parent, 0o750); err != nil {
		return false, err
	}
	staging, err := s.mkdirTemp(parent, "."+filepath.Base(target)+".seed-*")
	if err != nil {
		return false, err
	}
	defer s.RemoveAll(staging)
	if err := s.populateSharedOpenClawNPM(source, staging); err != nil {
		return false, err
	}
	if err := s.Rename(staging, target); err != nil {
		if _, targetErr := s.Lstat(target); targetErr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *openClawIO) populateSharedOpenClawNPM(source, target string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if entry.Name() == "node_modules" {
			if err := s.populateSharedNodeModules(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if entry.Name() == "projects" {
			if err := s.populateSharedNPMProjects(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := s.copyOpenClawNPMRootEntry(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

// populateSharedNPMProjects supports OpenClaw 2026.7.1's isolated managed npm
// layout. Project metadata remains instance-owned so OpenClaw can update its
// lock and retention state, while the image-provided packages are linked in
// the same way as packages from the legacy npm/node_modules layout.
func (s *openClawIO) populateSharedNPMProjects(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", source)
	}
	if err := s.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if !entry.IsDir() {
			if err := s.copyOpenClawNPMRootEntry(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := s.populateSharedNPMProject(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *openClawIO) populateSharedNPMProject(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", source)
	}
	if err := s.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if entry.Name() == "node_modules" {
			if err := s.populateSharedNodeModules(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := s.copyOpenClawNPMRootEntry(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *openClawIO) populateSharedNodeModules(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", source)
	}
	if err := s.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if entry.Name() == ".bin" && entry.IsDir() {
			if err := s.copyDir(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(entry.Name(), "@") && entry.IsDir() {
			if err := s.linkOpenClawPackageScope(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := s.linkOpenClawPackageEntry(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *openClawIO) linkOpenClawPackageScope(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := s.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := s.linkOpenClawPackageEntry(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *openClawIO) copyOpenClawNPMRootEntry(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return s.Symlink(linkTarget, target)
	}
	if info.IsDir() {
		return s.copyDir(source, target)
	}
	if info.Mode().IsRegular() {
		return s.copyRegularFile(source, target, info.Mode().Perm())
	}
	return nil
}

func (s *openClawIO) linkOpenClawPackageEntry(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return s.Symlink(linkTarget, target)
	}
	if info.IsDir() {
		return s.Symlink(filepath.Clean(source), target)
	}
	if info.Mode().IsRegular() {
		return s.copyRegularFile(source, target, info.Mode().Perm())
	}
	return nil
}

func (s *openClawIO) ensureDingTalkOpenClawPeer(activeRoot string) error {
	nodeModulesDir := filepath.Join(activeRoot, "npm", "node_modules")
	connectorDir := filepath.Join(nodeModulesDir, "@dingtalk-real-ai", "dingtalk-connector")
	info, err := s.Lstat(connectorDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat DingTalk connector: %w", err)
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("DingTalk connector path is not a directory: %s", connectorDir)
	}

	linkPath := filepath.Join(nodeModulesDir, "openclaw")
	linkInfo, err := s.Lstat(linkPath)
	if err == nil {
		if linkInfo.Mode()&os.ModeSymlink == 0 {
			// A real package also satisfies Node's peer dependency lookup. Preserve
			// instance-owned content instead of replacing it with the image link.
			return nil
		}
		currentTarget, readErr := s.Readlink(linkPath)
		if readErr != nil {
			return fmt.Errorf("read OpenClaw peer symlink: %w", readErr)
		}
		if currentTarget == openClawGlobalPackageDir {
			return nil
		}
		if removeErr := s.Remove(linkPath); removeErr != nil {
			return fmt.Errorf("replace OpenClaw peer symlink: %w", removeErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat OpenClaw peer path: %w", err)
	}

	if err := s.MkdirAll(nodeModulesDir, 0o750); err != nil {
		return fmt.Errorf("create OpenClaw peer directory: %w", err)
	}
	if err := s.Symlink(openClawGlobalPackageDir, linkPath); err != nil {
		if os.IsExist(err) {
			currentTarget, readErr := s.Readlink(linkPath)
			if readErr == nil && currentTarget == openClawGlobalPackageDir {
				return nil
			}
		}
		return fmt.Errorf("create OpenClaw peer symlink: %w", err)
	}
	return nil
}
func (s *openClawIO) copyOpenClawPluginDirIfMissing(source, target string) (bool, error) {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("source is not a directory: %s", source)
	}
	if _, err := s.Lstat(target); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	parent := filepath.Dir(target)
	if err := s.MkdirAll(parent, 0o750); err != nil {
		return false, err
	}
	staging, err := s.mkdirTemp(parent, "."+filepath.Base(target)+".seed-*")
	if err != nil {
		return false, err
	}
	defer s.RemoveAll(staging)
	if err := s.copyDir(source, staging); err != nil {
		return false, err
	}
	if err := s.Rename(staging, target); err != nil {
		if _, targetErr := s.Lstat(target); targetErr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *openClawIO) rewriteOpenClawPluginRegistry(registryPath, defaultsRoot, activeRoot string) error {
	data, err := s.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read OpenClaw plugin registry: %w", err)
	}

	var registry any
	if err := json.Unmarshal(data, &registry); err != nil {
		return fmt.Errorf("parse OpenClaw plugin registry: %w", err)
	}
	rewritten, changed := rewriteOpenClawPluginPaths(registry, defaultsRoot, activeRoot)
	if changed {
		encoded, err := json.MarshalIndent(rewritten, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal OpenClaw plugin registry: %w", err)
		}
		if err := s.WriteFile(registryPath, append(encoded, '\n'), 0o600); err != nil {
			return fmt.Errorf("write OpenClaw plugin registry: %w", err)
		}
	}
	if err := s.Chmod(registryPath, 0o600); err != nil {
		return fmt.Errorf("chmod OpenClaw plugin registry: %w", err)
	}
	return nil
}

func rewriteOpenClawPluginPaths(value any, defaultsRoot, activeRoot string) (any, bool) {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			rewritten, childChanged := rewriteOpenClawPluginPaths(child, defaultsRoot, activeRoot)
			typed[key] = rewritten
			changed = changed || childChanged
		}
		return typed, changed
	case []any:
		for i, child := range typed {
			rewritten, childChanged := rewriteOpenClawPluginPaths(child, defaultsRoot, activeRoot)
			typed[i] = rewritten
			changed = changed || childChanged
		}
		return typed, changed
	case string:
		return rewriteOpenClawPathPrefix(typed, defaultsRoot, activeRoot)
	default:
		return value, false
	}
}

func rewriteOpenClawPathPrefix(value, prefix, replacement string) (string, bool) {
	cleanValue := path.Clean(filepath.ToSlash(value))
	cleanPrefix := strings.TrimSuffix(path.Clean(filepath.ToSlash(prefix)), "/")
	cleanReplacement := strings.TrimSuffix(path.Clean(filepath.ToSlash(replacement)), "/")
	if cleanValue == cleanPrefix {
		return cleanReplacement, true
	}
	prefixWithSlash := cleanPrefix + "/"
	if !strings.HasPrefix(cleanValue, prefixWithSlash) {
		return value, false
	}
	return cleanReplacement + "/" + strings.TrimPrefix(cleanValue, prefixWithSlash), true
}
