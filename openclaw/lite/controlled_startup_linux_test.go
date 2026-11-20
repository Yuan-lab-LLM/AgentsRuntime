//go:build linux

package openclaw

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func init() {
	if os.Getenv("CM_CONTROLLED_START_TEST_CHILD") == "1" && len(os.Args) > 1 && os.Args[1] == "openclaw-controlled-gateway-start" {
		fmt.Printf("before_pid=%d\n", os.Getpid())
		_, err := NewProfile("openclaw").RunCommand(os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func TestOpenClawControlledGatewayStartRetainsTeamCommand(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_ROLE", "slot")
	command := []string{"openclaw", "gateway", "run", "--allow-unconfigured"}
	env := []string{"CLAWMANAGER_TEAM_ENABLED=true", "CLAWMANAGER_TEAM_UMASK=0002", "CLAWMANAGER_OPENCLAW_OWNER_WAIT_MS=15000"}
	want := append([]string{"/usr/local/bin/clawmanager-agent", "openclaw-controlled-gateway-start", "15000", "--"}, gateway.LiteTeamGatewayCommand("openclaw", command, env)...)
	if got := NewProfile("openclaw").GatewayProcessCommand(command, env); !reflect.DeepEqual(got, want) {
		t.Fatalf("native gateway bypasses controlled admission or changes Team command: %q", got)
	}
	for _, typ := range []string{"openclaw", "openclaw-shell"} {
		custom := []string{"/usr/bin/true"}
		if got := NewProfile(typ).GatewayProcessCommand(custom, nil); !reflect.DeepEqual(got, custom) {
			t.Fatalf("custom %s command changed: %q", typ, got)
		}
	}
	for _, role := range []string{"", "pool"} {
		t.Setenv("RUNTIME_AGENT_ROLE", role)
		if got := NewProfile("openclaw").GatewayProcessCommand(command, env); !reflect.DeepEqual(got, gateway.LiteTeamGatewayCommand("openclaw", command, env)) {
			t.Fatalf("non-slot gateway command changed: role=%q command=%q", role, got)
		}
	}
}

func TestOpenClawControlledGatewayWaitLeavesStartupBudget(t *testing.T) {
	for _, timeout := range []time.Duration{0, 30 * time.Second, 90 * time.Second, 5 * time.Minute} {
		cfg := gateway.Config{RuntimeType: "openclaw", GatewayStartupTimeout: timeout}
		req := gateway.CreateGatewayRequest{InstanceID: 1, UserID: 1, Environment: map[string]string{"CLAWMANAGER_OPENCLAW_OWNER_WAIT_MS": "99999999"}}
		env := OpenClawGatewayEnv(nil, cfg, req, "/workspaces/openclaw/user-1/instance-1", 20000)
		raw := gateway.EnvironmentValue(env, "CLAWMANAGER_OPENCLAW_OWNER_WAIT_MS")
		actual, err := strconv.ParseInt(raw, 10, 64)
		if timeout == 0 {
			timeout = NewProfile("openclaw").Defaults().GatewayStartupTimeout
		}
		want := min(120*time.Second, timeout/2).Milliseconds()
		if err != nil || actual != want || time.Duration(actual)*time.Millisecond >= timeout {
			t.Fatalf("wait budget must fit startup and override instance input: got=%q want=%d timeout=%v err=%v", raw, want, timeout, err)
		}
	}
}

func TestOpenClawControlledGatewayExecPreservesPIDAndTeamUmask(t *testing.T) {
	if os.Getenv("CM_TEST_OPENCLAW_CONTROLLED_EXEC") != "1" {
		t.Skip("run the acceptance binary with native Node in an isolated server54 container fixture")
	}
	home := t.TempDir()
	trap := filepath.Join(home, "untrusted-loader.mjs")
	marker := filepath.Join(home, "untrusted-loader-ran")
	if err := os.WriteFile(trap, []byte("import fs from 'node:fs';fs.writeFileSync("+strconv.Quote(marker)+",'bad');"), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAWMANAGER_TEAM_ENABLED=true", "CLAWMANAGER_TEAM_UMASK=0002"}
	command := gateway.LiteTeamGatewayCommand("openclaw", []string{"/bin/sh", "-c", `printf 'after_pid=%s value=%s umask=%s node_options=%s\n' "$$" "$CONTROLLED_EXEC_VALUE" "$(umask)" "$NODE_OPTIONS"`}, env)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, append([]string{controlledGatewayStart, "15000", "--"}, command...)...)
	child.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "OPENCLAW_STATE_DIR=" + filepath.Join(home, ".openclaw"), "CM_CONTROLLED_START_TEST_CHILD=1", "CONTROLLED_EXEC_VALUE=business-value", "NODE_OPTIONS=--import=" + trap, "RUNTIME_AGENT_CONTROL_TOKEN=private-test-sentinel"}
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("controlled exec failed: %v %s", err, out)
	}
	var before, after int
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "before_pid=") {
			_, _ = fmt.Sscanf(line, "before_pid=%d", &before)
		}
		if strings.HasPrefix(line, "after_pid=") {
			_, _ = fmt.Sscanf(line, "after_pid=%d", &after)
		}
	}
	if before == 0 || before != after || !strings.Contains(string(out), "value=business-value umask=0002") || !strings.Contains(string(out), "node_options=--import="+trap) {
		t.Fatalf("exec changed PID, business environment or Team umask: %s", out)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) || strings.Contains(string(out), "private-test-sentinel") {
		t.Fatal("guard admitted a business loader or exposed a management credential")
	}
}
