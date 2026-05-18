package bootstrap

import (
	"fmt"
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
		if err := os.MkdirAll(path, 0o775); err != nil {
			return fmt.Errorf("mkdir %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o775); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}

	if os.Geteuid() == 0 && cfg.DropUserName != "" {
		uid, gid, err := lookupDropUser(cfg.DropUserName)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if err := os.Chown(path, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", path, err)
			}
		}
	}
	return nil
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
