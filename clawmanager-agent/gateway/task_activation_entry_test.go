package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenClawCompatActivationBeforeWorkspaceWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	req := CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL": "invalid"}}
	err := (openClawCompatProfile{}).PrepareWorkspace(Config{WorkspaceRoot: root}, req, root)
	if err == nil || !strings.Contains(err.Error(), "启用校验") {
		t.Fatalf("gate not reached: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("workspace was modified before activation check")
	}
}
