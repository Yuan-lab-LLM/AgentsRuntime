package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateWorkspacePathAcceptsOnlyExpectedInstanceDirectory(t *testing.T) {
	root := t.TempDir()
	req := CreateGatewayRequest{
		InstanceID:    123,
		UserID:        45,
		AgentType:     "openclaw",
		WorkspacePath: filepath.Join(root, "openclaw", "user-45", "instance-123"),
	}

	got, err := ValidateWorkspacePath(root, "openclaw", req)
	if err != nil {
		t.Fatalf("ValidateWorkspacePath() error = %v", err)
	}
	if got != filepath.Clean(req.WorkspacePath) {
		t.Fatalf("ValidateWorkspacePath() = %q, want cleaned workspace path", got)
	}
}

func TestValidateWorkspacePathRejectsEscapesAndWrongInstance(t *testing.T) {
	root := t.TempDir()
	base := CreateGatewayRequest{
		InstanceID: 123,
		UserID:     45,
		AgentType:  "openclaw",
	}

	wrongInstance := base
	wrongInstance.WorkspacePath = filepath.Join(root, "openclaw", "user-45", "instance-124")
	if _, err := ValidateWorkspacePath(root, "openclaw", wrongInstance); err == nil {
		t.Fatal("ValidateWorkspacePath() accepted wrong instance path")
	}

	escape := base
	escape.WorkspacePath = filepath.Join(root, "openclaw", "user-45", "instance-123", "..", "instance-124")
	if _, err := ValidateWorkspacePath(root, "openclaw", escape); err == nil {
		t.Fatal("ValidateWorkspacePath() accepted parent-directory escape")
	}
}

func TestPrepareWorkspaceKeepsIntermediateDirectoriesTraversable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission mode bits are not portable on Windows")
	}

	root := t.TempDir()
	req := CreateGatewayRequest{
		InstanceID:    123,
		UserID:        45,
		AgentType:     "hermes",
		WorkspacePath: filepath.Join(root, "hermes", "user-45", "instance-123"),
	}

	if _, err := PrepareWorkspace(root, "hermes", req); err != nil {
		t.Fatalf("PrepareWorkspace() error = %v", err)
	}

	assertMode(t, filepath.Join(root, "hermes"), 0o755)
	assertMode(t, filepath.Join(root, "hermes", "user-45"), 0o755)
	assertMode(t, req.WorkspacePath, 0o750)
	assertMode(t, filepath.Join(req.WorkspacePath, "home"), 0o750)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %v, want %v", path, got, want)
	}
}
