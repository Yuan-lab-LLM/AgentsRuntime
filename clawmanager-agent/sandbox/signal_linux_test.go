//go:build linux

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestScopedPinnedSignalRestoresManagementIdentity(t *testing.T) {
	managementAcceptance := os.Getenv("CM_SCOPED_SIGNAL_ACCEPTANCE") == "1" && os.Geteuid() == 10001
	if os.Geteuid() != 0 && !managementAcceptance {
		t.Skip("requires disposable root test runner for a distinct business UID")
	}
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 271001, Gid: 271001}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer SignalProcess(cmd.Process, 271001, syscall.SIGKILL)
	before := os.Geteuid()
	signal := signalProcessAsUID
	if managementAcceptance {
		if err := cmd.Process.Signal(syscall.SIGWINCH); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("acceptance requires no outer KILL capability: %v", err)
		}
		signal = SignalProcess
	}
	if err := signal(cmd.Process, 271001, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != before {
		t.Fatal("temporary signal identity escaped its locked thread")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("pinned business process was not terminated")
	}
}
