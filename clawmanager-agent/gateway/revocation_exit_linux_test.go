//go:build linux

package gateway

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestRevocationCleanupRealPrivateProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $!; exit 78")
	configureGatewayCommand(cmd, 0, 0)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || child <= 1 {
		t.Fatal("invalid test child")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	exit := cmd.Wait()
	spec := GatewayStartSpec{RuntimeType: "hermes", Env: []string{
		"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE=1",
		"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL=1",
	}}
	result := cleanupRevocationExit(spec, cmd, exit)
	// An unreaped zombie keeps its group observable: conservative failure is
	// required in that case, rather than releasing the port and restarting.
	if result != exit && !errors.Is(result, ErrGatewayStopFailed) {
		t.Fatalf("unexpected cleanup result: %v", result)
	}
	raw, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
	if readErr == nil {
		end := strings.LastIndex(string(raw), ") ")
		if end < 0 || !strings.HasPrefix(string(raw)[end+2:], "Z ") {
			t.Fatalf("tool process remains live after cleanup")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestRevocationCleanupRefusesUnknownProcessGroup(t *testing.T) {
	if err := cleanupExitedGatewayGroup(&exec.Cmd{}); err == nil {
		t.Fatal("unowned group accepted")
	}
}
