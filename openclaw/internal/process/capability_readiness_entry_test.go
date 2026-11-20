package process

import (
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOpenClawReadinessChild(t *testing.T) {
	if os.Getenv("CM_READINESS_TEST_CHILD") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
}
func TestOpenClawCapabilityTimeoutNeverPromotesRunning(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenClawReadinessChild$")
	cmd.Env = append(os.Environ(), "CM_READINESS_TEST_CHILD=1", renewalFlag+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	manager := New(appconfig.Config{OpenClawStartupHealthTimeout: 10 * time.Millisecond})
	manager.cmd = cmd
	manager.status = StatusStarting
	manager.promoteRunning(cmd.Process.Pid, false)
	if err := cmd.Wait(); err == nil {
		t.Fatal("unready child not terminated")
	}
	if manager.status == StatusRunning || manager.lastOperationResult != "OpenClaw capability startup readiness timed out" {
		t.Fatal("unready child promoted or reason lost")
	}
}
