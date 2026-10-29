package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
)

func newFixtureUpgradeIO(values ...any) *upgradeIO {
	io := &upgradeIO{}
	for _, value := range values {
		path, ok := value.(string)
		if !ok || !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		for {
			info, err := os.Lstat(path)
			if err == nil && info.IsDir() {
				break
			}
			next := filepath.Dir(path)
			if next == path {
				break
			}
			path = next
		}
		if root, err := workspaceio.Open(path); err == nil {
			io.scopes = append(io.scopes, upgradeScope{root, true})
			if io.pool == "" {
				io.pool = path
			}
		}
	}
	return io
}
