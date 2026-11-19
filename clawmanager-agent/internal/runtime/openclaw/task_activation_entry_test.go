package openclaw_test

import (
	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/runtime/openclaw"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenClawActivationBeforeWorkspaceWrites(t *testing.T) {
	for _, flags := range []map[string]string{
		{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL": "invalid"},
		{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL": "0", "CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL": "1"},
		{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL": "0", "CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT": "1"},
	} {
		for _, kind := range []string{"openclaw", "openclaw-shell"} {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			req := gateway.CreateGatewayRequest{Environment: flags}
			err := openclaw.NewProfile(kind).PrepareWorkspace(gateway.Config{WorkspaceRoot: root}, req, root)
			if err == nil || !strings.Contains(err.Error(), "启用校验") {
				t.Fatalf("gate not reached: %v", err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("workspace modified before gate")
			}
		}
	}
}
