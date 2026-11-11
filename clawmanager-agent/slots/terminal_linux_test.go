//go:build linux

package slots

import (
	"context"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"github.com/iamlovingit/clawmanager-agent/workspacegit"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxPTYInputResizeAndIndependentCwd(t *testing.T) {
	for _, cwd := range []string{t.TempDir(), t.TempDir()} {
		t.Run(cwd, func(t *testing.T) {
			master, slave, err := openTerminal(80, 24)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			cmd := exec.Command("/bin/sh", "-i")
			cmd.Dir = cwd
			cmd.Stdin = slave
			cmd.Stdout = slave
			cmd.Stderr = slave
			configureTerminal(cmd)
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			slave.Close()
			defer cmd.Process.Kill()
			if err = resizeTerminal(master, 103, 37); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() { <-ctx.Done(); master.Close() }()
			if _, err = io.WriteString(master, "pwd; stty size; printf 'done-marker\\n'; exit\n"); err != nil {
				t.Fatal(err)
			}
			output, _ := io.ReadAll(master)
			_ = cmd.Wait()
			if !strings.Contains(string(output), cwd) || !strings.Contains(string(output), "37 103") || !strings.Contains(string(output), "done-marker") {
				t.Fatalf("PTY output: %q", output)
			}
		})
	}
}

func TestLinuxWorkbenchSandboxTerminal(t *testing.T) {
	for _, profile := range []string{"", "nfs-rootless-v1"} {
		t.Run(profile, func(t *testing.T) { runLinuxWorkbenchSandboxTerminal(t, profile) })
	}
}
func runLinuxWorkbenchSandboxTerminal(t *testing.T, profile string) {
	if os.Getenv("CM_WORKBENCH_SANDBOX_ACCEPTANCE") != "1" {
		t.Skip("requires privileged Linux sandbox gate")
	}
	if os.Geteuid() != 0 {
		t.Fatal("gate requires root supervisor")
	}
	root, err := os.MkdirTemp("/", "workbench-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	os.Chmod(root, 0755)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	if err = os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	os.Chown(workspace, 200002, 200002)
	os.Mkdir(filepath.Join(workspace, "project"), 0755)
	os.Chown(filepath.Join(workspace, "project"), 200002, 200002)
	if profile != "" {
		for _, dir := range []string{root, filepath.Join(root, "openclaw"), filepath.Join(root, "openclaw", "user-1"), filepath.Join(root, ".clawmanager-instance-tmp"), filepath.Join(root, ".clawmanager-instance-tmp", "openclaw"), filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1"), filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2")} {
			os.MkdirAll(dir, 0755)
			os.Chown(dir, 10001, 10001)
		}
		tmp := filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2", "tmp")
		os.Mkdir(tmp, 0777)
		os.Chown(tmp, 200002, 200002)
		os.Chmod(tmp, os.ModeSticky|0777)
	}
	cfg := gateway.Config{SandboxProfile: profile, WorkspaceRoot: root, RuntimeType: "openclaw", IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}, ProcessStopTimeout: time.Second}
	starter := sandboxStarter{cfg: cfg}
	spec := gateway.GatewayStartSpec{GatewayID: "gw-2-1", InstanceID: 2, UserID: 1, Generation: 1, RuntimeType: "openclaw", UID: 200002, GID: 200002, WorkspacePath: workspace, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + workspace, "EXPLICIT_BUSINESS=visible", "RUNTIME_AGENT_CONTROL_TOKEN=hidden"}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	terminal, err := starter.StartGatewayTerminal(ctx, spec, workbench.TerminalOptions{Cwd: "project", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.IO.Close()
	if err = terminal.Resize(104, 38); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	readDone := make(chan struct{})
	go func() { io.Copy(&output, terminal.IO); close(readDone) }()
	_, err = io.WriteString(terminal.IO, "pwd; id -u; stty size; test \"$(ps -p $$ -o sid= | tr -d ' ')\" -gt 0 && echo terminal-session-owned; echo $EXPLICIT_BUSINESS; echo ${RUNTIME_AGENT_CONTROL_TOKEN-unset}; setsid /bin/sh -c 'trap \"\" HUP TERM; while :; do printf x >> \"$HOME/writes\"; sleep 0.03; done' >/dev/null 2>&1 & sleep 0.1; exit\n")
	if err != nil {
		t.Fatal(err)
	}
	waitErr := terminal.Wait()
	<-readDone
	if waitErr != nil {
		t.Fatalf("sandbox terminal: %v output=%q", waitErr, output.String())
	}
	for _, expected := range []string{workspace + "/project", "200002", "38 104", "visible", "unset", "\r\nterminal-session-owned\r\n"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s: %q", expected, output.String())
		}
	}
	before, err := os.ReadFile(filepath.Join(workspace, "writes"))
	if err != nil || len(before) == 0 {
		t.Fatalf("detached writer fixture did not start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadFile(filepath.Join(workspace, "writes"))
	if string(before) != string(after) {
		t.Fatal("terminal left a detached writer alive")
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gitSpec := spec
	gitSpec.Command = []string{"/usr/bin/git", "init", "-q", filepath.Join(workspace, "project")}
	if _, err = starter.RunGatewayCommand(ctx, gitSpec); err != nil {
		t.Fatalf("business git init: %v", err)
	}
	gitSpec.Command = []string{executable, workbench.GitCommand}
	gitSpec.OutputLimit = workbench.MaxOutput
	gitSpec.Stdin, _ = json.Marshal(workbench.GitPayload{Root: workspace, Request: workspacegit.Request{Operation: "import", Path: "project"}})
	raw, err := starter.RunGatewayCommand(ctx, gitSpec)
	if err != nil {
		t.Fatalf("business git helper: %v", err)
	}
	var envelope workbench.GitEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error != nil || envelope.Result == nil || envelope.Result.Project == nil || envelope.Result.Project.Path != "project" {
		t.Fatalf("git helper response: %s", raw)
	}
	// Reopening after disconnect uses the same persistent workspace; escape paths fail.
	os.Symlink("/", filepath.Join(workspace, "escape"))
	if _, err = starter.StartGatewayTerminal(ctx, spec, workbench.TerminalOptions{Cwd: "escape"}); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
