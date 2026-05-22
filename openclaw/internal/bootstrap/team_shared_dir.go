package bootstrap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

var teamSharedSubdirs = []string{
	"inbox",
	"status",
	"tasks",
	"results",
	".openclaw-redis-team",
}

const teamSharedDirMode fs.FileMode = fs.ModeSetgid | 0o775

func ensureTeamSharedDirs(cfg appconfig.Config) error {
	if !teamEnabledFromEnv() {
		return nil
	}

	root := strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_SHARED_DIR"))
	if root == "" {
		root = "/team"
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("CLAWMANAGER_TEAM_SHARED_DIR must be absolute, got %q", root)
	}

	paths := append([]string{root}, teamSharedSubdirPaths(root)...)
	for _, path := range paths {
		if err := os.MkdirAll(path, teamSharedDirMode); err != nil {
			return fmt.Errorf("mkdir %s: %w", path, err)
		}
		if err := os.Chmod(path, teamSharedDirMode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}

	if os.Geteuid() == 0 && cfg.DropUserName != "" {
		uid, gid, err := lookupDropUser(cfg.DropUserName)
		if err != nil {
			return err
		}
		if err := repairTeamSharedTree(root, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func repairTeamSharedTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			if err := os.Chmod(path, teamSharedDirMode); err != nil {
				return fmt.Errorf("chmod %s: %w", path, err)
			}
			return nil
		}
		mode := info.Mode().Perm() | 0o660
		if info.Mode().Perm()&0o111 != 0 {
			mode |= 0o110
		}
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		return nil
	})
}

func teamSharedSubdirPaths(root string) []string {
	paths := make([]string, 0, len(teamSharedSubdirs))
	for _, dir := range teamSharedSubdirs {
		paths = append(paths, filepath.Join(root, dir))
	}
	return paths
}

func teamEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
