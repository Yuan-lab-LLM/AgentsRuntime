package bootstrap

import (
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/receiptstorage"
	"os"
	"path/filepath"
)

func prepareTaskReceiptStorage(cfg appconfig.Config) error {
	root := os.Getenv(receiptstorage.RootEnv)
	if root == "" {
		return nil
	}
	uid, gid, err := lookupDropUser(cfg.DropUserName)
	if err != nil {
		return err
	}
	id := receiptstorage.Identity{InstanceID: cfg.InstanceID, OwnerID: os.Getenv("USER_ID"), UID: uid, GID: gid}
	return receiptstorage.Ensure(root, id, true, cfg.DiskUsagePath, cfg.AgentDataDir, filepath.Dir(cfg.OpenClawConfigPath), cfg.OpenClawWorkspacePath)
}
