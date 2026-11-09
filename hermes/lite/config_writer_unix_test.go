//go:build !windows

package hermes

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestWriteGatewayConfigChownsHermesHomeContents(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to chown files to an arbitrary instance uid")
	}

	workspace := filepath.Join(t.TempDir(), "hermes", "user-45", "instance-63")
	req := gateway.CreateGatewayRequest{
		InstanceID: 63,
		UserID:     45,
		UID:        246810,
		GID:        246810,
		Environment: map[string]string{
			"CLAWMANAGER_LLM_BASE_URL": "http://127.0.0.1:9001/api/v1/gateway/llm",
			"CLAWMANAGER_LLM_API_KEY":  "instance-token",
			"CLAWMANAGER_LLM_MODEL":    `["auto"]`,
		},
	}
	cfg := gateway.Config{
		RuntimeType:     "hermes",
		GatewayAuthMode: "trusted-proxy",
	}

	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	hermesHome := filepath.Join(workspace, "home", ".hermes")
	for _, path := range []string{
		hermesHome,
		filepath.Join(hermesHome, "config.yaml"),
		filepath.Join(hermesHome, ".env"),
		filepath.Join(hermesHome, "gateway.json"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("stat %s did not return syscall.Stat_t", path)
		}
		if stat.Uid != uint32(req.UID) || stat.Gid != uint32(req.GID) {
			t.Fatalf("owner %s = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, req.UID, req.GID)
		}
	}
}
