package process

import (
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/receiptstorage"
	"os"
	"path/filepath"
	"strings"
)

func proReceiptEnvironment(cfg appconfig.Config, base []string) ([]string, error) {
	env := append([]string(nil), base...)
	root := proReceiptEnvValue(env, receiptstorage.RootEnv)
	if root == "" {
		return env, nil
	}
	id := receiptstorage.Identity{InstanceID: cfg.InstanceID, OwnerID: proReceiptEnvValue(env, "USER_ID"), UID: os.Geteuid(), GID: os.Getegid()}
	if err := receiptstorage.Ensure(root, id, false, cfg.DiskUsagePath, cfg.AgentDataDir, filepath.Dir(cfg.OpenClawConfigPath), cfg.OpenClawWorkspacePath); err != nil {
		return nil, err
	}
	env = removeEnv(env, receiptstorage.RootEnv)
	env = removeEnv(env, receiptstorage.DirectoryEnv)
	return append(env, receiptstorage.DirectoryEnv+"="+receiptstorage.Directory(root)), nil
}
func proReceiptEnvValue(env []string, key string) string {
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == key {
			return value
		}
	}
	return ""
}
