package bootstrap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	if err := writeInitialTeamStatus(root); err != nil {
		return err
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

func writeInitialTeamStatus(root string) error {
	memberID := firstNonEmptyEnv("CLAWMANAGER_TEAM_MEMBER_ID", "CLAWMANAGER_TEAM_ROLE")
	if memberID == "" {
		return nil
	}
	role := strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_ROLE"))
	if role == "" {
		role = memberID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	status := map[string]any{
		"teamId":       strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_ID")),
		"memberId":     memberID,
		"role":         role,
		"liveness":     "starting",
		"runtime":      "starting",
		"availability": "idle",
		"lastSeenAt":   now,
		"updatedAt":    now,
		"source":       "openclaw-agent-bootstrap",
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal initial team status: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "status", safeTeamStatusName(memberID)+".json")
	if err := os.WriteFile(path, data, 0o664); err != nil {
		return fmt.Errorf("write initial team status %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		return fmt.Errorf("chmod initial team status %s: %w", path, err)
	}
	return nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func safeTeamStatusName(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '.' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 160 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
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
