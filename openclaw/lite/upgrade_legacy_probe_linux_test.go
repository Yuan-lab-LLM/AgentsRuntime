//go:build linux

package openclaw

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyUpgradeProbeRunsAsAssignedUserWithoutReadingControllerEvidence(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("actual credential transition requires root")
	}
	m, req, workspace := scopedUpgradeService(t)
	// These are only Go's exact test-owned temporary parents.
	for _, dir := range []string{filepath.Dir(m.cfg.WorkspaceRoot), m.cfg.WorkspaceRoot} {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(m.io.controlRoot(), "controller-evidence")
	if err := m.io.AtomicWriteFile(secret, []byte("private-evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	probe, home, _, err := m.createUpgradeCompatibilityProbe(workspace, map[string]any{}, req)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(m.cfg.WorkspaceRoot, "test-bin")
	if err = os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -eu\ntest -r \"$OPENCLAW_CONFIG_PATH\"\ntest ! -r '" + secret + "'\nprintf 'probe-user-ok'\n"
	if err = os.WriteFile(filepath.Join(bin, "openclaw"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var hash bytes.Buffer
	if err = m.runOpenClawCommandInHome(context.Background(), probe, home, filepath.Join(home, ".openclaw/openclaw.json"), req, "config-validation", nil, &hash, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(hash.Bytes(), []byte("probe-user-ok")) {
		t.Fatal("assigned-user command did not execute")
	}
	info, err := os.Stat(m.io.controlRoot())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0066 != 0 {
		t.Fatal("controller evidence directory became readable or writable")
	}
}
