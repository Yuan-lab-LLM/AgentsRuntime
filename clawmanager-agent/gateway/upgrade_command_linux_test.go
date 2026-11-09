//go:build linux

package gateway

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUpgradeCommandCancellationStopsChildWriter(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `(sleep 1; echo unexpected > "$1") & echo ready; wait`, "fixture", filepath.Join(dir, "late-write"))
	configureGatewayCommand(cmd, 0, 0)
	cleanup := fenceUpgradeCommand(cmd)
	defer cleanup()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var buf [6]byte
	if _, err := stdout.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command succeeded")
	}
	cleanup()
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late-write")); !os.IsNotExist(err) {
		t.Fatalf("child continued writing: %v", err)
	}
}
