package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigWriterDoesNotReadOrMutateSiblingSymlink(t *testing.T) {
	t.Setenv("CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED", "false")
	workspace := t.TempDir()
	victim := filepath.Join(t.TempDir(), "secret.yaml")
	original := []byte("private: sibling\n")
	os.WriteFile(victim, original, 0600)
	home := filepath.Join(workspace, "home", ".hermes")
	os.MkdirAll(home, 0750)
	os.Symlink(victim, filepath.Join(home, "config.yaml"))
	err := WriteGatewayConfig(gateway.Config{}, gateway.CreateGatewayRequest{}, workspace)
	if err == nil {
		t.Fatal("external config symlink accepted")
	}
	after, readErr := os.ReadFile(victim)
	if readErr != nil || string(after) != string(original) {
		t.Fatal("sibling config changed")
	}
}
