package netguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequiredRootlessToolsRejectMissingAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "ip")
	if err := validateRequiredTools(tool); err == nil {
		t.Fatal("missing runtime tool accepted")
	}
	if err := os.WriteFile(tool, []byte("fixed test tool"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateRequiredTools(tool); err == nil {
		t.Fatal("nonexecutable runtime tool accepted")
	}
	if err := os.Chmod(tool, 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateRequiredTools(tool); err != nil {
		t.Fatal(err)
	}
	if err := validateRequiredTools(dir); err == nil {
		t.Fatal("executable directory accepted as runtime tool")
	}
}
