package opencode

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewConfigWriterMustNotFollowSiblingSymlink(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "other-instance-secret.json")
	original := []byte("private sibling state")
	if err := os.WriteFile(victim, original, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "opencode", "user-1", "instance-2")
	configDir := filepath.Join(workspace, "home", ".opencode")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(configDir, "opencode.json")); err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{UID: 200001, GID: 200001, Environment: map[string]string{"CLAWMANAGER_LLM_BASE_URL": "http://gateway.example/v1", "CLAWMANAGER_LLM_API_KEY": "business-key", "CLAWMANAGER_LLM_MODEL": "auto"}}
	_ = WriteGatewayConfig(gateway.Config{}, req, workspace)
	after, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("privileged config writer overwrote sibling target through a user-controlled symlink")
	}
}
