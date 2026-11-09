package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const bundledRedisTeamPluginID = "redis-team"
const bundledRedisTeamPackageName = "@clawmanager/openclaw-redis-team"

// syncDefaults copies cfg.OpenClawDefaultsDir into the parent directory of
// cfg.OpenClawConfigPath when the state directory or the active config file
// is missing. The copy preserves file modes and skips targets that already
// exist so we never overwrite a user's on-disk /config/.openclaw state.
func syncDefaults(cfg appconfig.Config) error {
	stateDir := filepath.Dir(cfg.OpenClawConfigPath)
	stateDirMissing := !pathExists(stateDir)
	configMissing := !pathExists(cfg.OpenClawConfigPath)
	if !stateDirMissing && !configMissing {
		return nil
	}

	if !pathExists(cfg.OpenClawDefaultsDir) {
		return fmt.Errorf("defaults source %q does not exist", cfg.OpenClawDefaultsDir)
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	return copyTreeIfMissing(cfg.OpenClawDefaultsDir, stateDir)
}

// ensureExtensionsDir makes the user extensions directory exist so external
// channel plugins have a predictable mount target.
func ensureExtensionsDir(cfg appconfig.Config) error {
	if cfg.OpenClawExtensionsDir == "" {
		return nil
	}
	return os.MkdirAll(cfg.OpenClawExtensionsDir, 0o755)
}

func syncBundledRedisTeamPlugin(cfg appconfig.Config) error {
	if !teamEnabledFromEnv() {
		return nil
	}
	if cfg.OpenClawDefaultsDir == "" || cfg.OpenClawExtensionsDir == "" {
		return nil
	}

	src := filepath.Join(cfg.OpenClawDefaultsDir, "extensions", bundledRedisTeamPluginID)
	dst := filepath.Join(cfg.OpenClawExtensionsDir, bundledRedisTeamPluginID)
	if !pathExists(src) {
		return nil
	}
	if err := validateManagedRedisTeamPlugin(src, true); err != nil {
		return fmt.Errorf("invalid managed source: %w", err)
	}
	if sameBundledPlugin(src, dst) {
		return nil
	}
	if pathExists(dst) {
		if err := validateManagedRedisTeamPlugin(dst, false); err != nil {
			return fmt.Errorf("preserve unrecognized existing redis-team extension: %w", err)
		}
	}
	if err := replaceTree(dst, src, cfg.OpenClawExtensionsDir); err != nil {
		return fmt.Errorf("replace %s: %w", bundledRedisTeamPluginID, err)
	}
	return nil
}

func validateManagedRedisTeamPlugin(root string, requireCurrentPackage bool) error {
	packageData, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return fmt.Errorf("read package.json: %w", err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageData, &pkg); err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}
	if pkg.Name != bundledRedisTeamPackageName {
		if requireCurrentPackage || pkg.Name != "@clawmanager/redis-team" {
			return fmt.Errorf("unexpected package name %q", pkg.Name)
		}
	}
	if pkg.Version == "" {
		return fmt.Errorf("package version is empty")
	}

	manifestData, err := os.ReadFile(filepath.Join(root, "openclaw.plugin.json"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest struct {
		ID       string   `json:"id"`
		Channels []string `json:"channels"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.ID != bundledRedisTeamPluginID {
		return fmt.Errorf("unexpected manifest id %q", manifest.ID)
	}
	hasChannel := false
	for _, channel := range manifest.Channels {
		if channel == bundledRedisTeamPluginID {
			hasChannel = true
			break
		}
	}
	if !hasChannel {
		return fmt.Errorf("manifest does not declare redis-team channel")
	}
	entry, err := os.Stat(filepath.Join(root, "dist", "index.js"))
	if err != nil {
		return fmt.Errorf("stat dist/index.js: %w", err)
	}
	if !entry.Mode().IsRegular() || entry.Size() == 0 {
		return fmt.Errorf("dist/index.js is missing or empty")
	}
	return nil
}

func sameBundledPlugin(src, dst string) bool {
	if !pathExists(dst) {
		return false
	}
	srcVersion := pluginPackageVersion(src)
	dstVersion := pluginPackageVersion(dst)
	if srcVersion != "" && dstVersion != "" && srcVersion != dstVersion {
		return false
	}
	for _, rel := range []string{"package.json", "openclaw.plugin.json", filepath.Join("dist", "index.js")} {
		s, err := fileSHA256(filepath.Join(src, rel))
		if err != nil {
			return false
		}
		d, err := fileSHA256(filepath.Join(dst, rel))
		if err != nil || s != d {
			return false
		}
	}
	return true
}

func pluginPackageVersion(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func replaceTree(dst, src, allowedRoot string) error {
	cleanRoot, err := filepath.Abs(filepath.Clean(allowedRoot))
	if err != nil {
		return err
	}
	cleanDst, err := filepath.Abs(filepath.Clean(dst))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(cleanRoot, cleanDst)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" || relHasParentPrefix(rel) {
		return fmt.Errorf("refusing to replace path outside extensions dir: %s", cleanDst)
	}
	if err := os.MkdirAll(cleanRoot, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(cleanRoot, ".redis-team-staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyTreeIfMissing(src, staging); err != nil {
		return err
	}
	if err := validateManagedRedisTeamPlugin(staging, true); err != nil {
		return fmt.Errorf("validate staged plugin: %w", err)
	}

	backup, err := os.MkdirTemp(cleanRoot, ".redis-team-backup-")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	defer os.RemoveAll(backup)
	hadDestination := pathExists(cleanDst)
	if hadDestination {
		if err := os.Rename(cleanDst, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, cleanDst); err != nil {
		if hadDestination {
			_ = os.Rename(backup, cleanDst)
		}
		return err
	}
	if hadDestination {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func relHasParentPrefix(rel string) bool {
	return rel == ".." || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)
}

func copyTreeIfMissing(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			return nil
		}

		if pathExists(target) {
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
			return nil
		}

		return copyRegularFile(path, target, info.Mode().Perm())
	})
}

func copyRegularFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dst, err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("copy %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	if err := os.Chmod(dst, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	return nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
