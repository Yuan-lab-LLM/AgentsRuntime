//go:build linux

package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This gate must run as the real slot supervisor inside its deployment security
// context. The separate nonroot gate tests the filesystem boundary itself.
func TestLinuxSupervisorAcceptance(t *testing.T) {
	if os.Getenv("CM_SANDBOX_SUPERVISOR_ACCEPTANCE") != "1" {
		t.Skip("run sandbox/acceptance-linux.sh --supervisor on Linux")
	}
	if os.Getuid() != 0 {
		t.Fatal("supervisor acceptance requires the root slot supervisor identity")
	}
	uid, gid := 200001, 200001
	for key, target := range map[string]*int{"CM_SANDBOX_UID": &uid, "CM_SANDBOX_GID": &gid} {
		if raw := os.Getenv(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 {
				t.Fatalf("invalid %s", key)
			}
			*target = value
		}
	}
	for _, tool := range []string{"bwrap", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.MkdirTemp("/tmp", "cm-sandbox-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	root, err := filepath.EvalSymlinks(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"graceful", "forced"} {
		t.Run(mode, func(t *testing.T) {
			workspace := filepath.Join(root, mode)
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(workspace, uid, gid); err != nil {
				t.Fatal(err)
			}
			script := `
test "$(id -u)" = "$1"
test "$(id -g)" = "$2"
test ! -e /proc/self/fd/3
test ! -e /proc/self/fd/4
if test "$3" = forced; then trap '' TERM; else trap 'printf flushed > /workspace/flushed; exit 0' TERM; fi
setsid /bin/sh -c 'trap "" TERM; while :; do printf x >> /workspace/writes; sleep 0.05; done' &
printf ready > /workspace/ready
while :; do sleep 0.05; done
`
			spec := Spec{Args: []string{"/bin/sh", "-eu", "-c", script, "supervisor-test", strconv.Itoa(uid), strconv.Itoa(gid), mode}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/workspace"}, Dir: "/workspace", UID: uid, GID: gid, Mounts: []Mount{{Source: workspace, Target: "/workspace"}}}
			args, err := Command(spec)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
			cmd.WaitDelay = 3 * time.Second
			var output strings.Builder
			cmd.Stdout, cmd.Stderr = &output, &output
			tracker, err := Track(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer tracker.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := tracker.Started(ctx); err != nil {
				t.Fatalf("root-to-assigned-UID startup: %v", err)
			}
			waitFor(t, func() bool { _, err := os.Stat(filepath.Join(workspace, "ready")); return err == nil }, "gateway signal handler")
			waitFor(t, func() bool { data, _ := os.ReadFile(filepath.Join(workspace, "writes")); return len(data) > 0 }, "detached writer")
			if err := tracker.Terminate(ctx); err != nil {
				t.Fatalf("root supervisor could not signal assigned UID (requires CAP_KILL and pidfd syscalls): %v", err)
			}
			if mode == "forced" {
				time.Sleep(150 * time.Millisecond)
				if err := cmd.Process.Kill(); err != nil {
					t.Fatalf("root supervisor could not force stop assigned UID: %v", err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if mode == "graceful" && err != nil {
					t.Fatalf("graceful exit failed: %v\n%s", err, output.String())
				}
			case <-ctx.Done():
				t.Fatal("root supervisor stop timed out")
			}
			if err := tracker.WaitStopped(ctx); err != nil {
				t.Fatalf("namespace teardown: %v", err)
			}
			if mode == "graceful" {
				if _, err := os.Stat(filepath.Join(workspace, "flushed")); err != nil {
					t.Fatal("gateway shutdown hook did not flush")
				}
			}
			before, _ := os.ReadFile(filepath.Join(workspace, "writes"))
			time.Sleep(150 * time.Millisecond)
			after, _ := os.ReadFile(filepath.Join(workspace, "writes"))
			if string(before) != string(after) {
				t.Fatal("detached child kept writing after supervisor stop")
			}
		})
	}
	t.Run("listener-attribution", func(t *testing.T) { acceptListenerAttribution(t, root, uid, gid) })
}

// The same test binary is copied into the authorized workspace, so this probe
// needs neither Go nor Python/Node installed in the final slim image.
func TestSandboxListenerHelper(t *testing.T) {
	if os.Getenv("CM_SANDBOX_LISTENER_HELPER") != "1" {
		t.Skip("private Linux acceptance helper")
	}
	port, err := strconv.Atoi(os.Getenv("CM_SANDBOX_LISTENER_PORT"))
	if err != nil || port < 1 {
		t.Fatal("invalid probe port")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.WriteFile("/workspace/ready", nil, 0600); err != nil {
		t.Fatal(err)
	}
	<-signals
	if err := os.WriteFile("/workspace/flushed", nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func acceptListenerAttribution(t *testing.T, root string, uid, gid int) {
	t.Helper()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	port := reservation.Addr().(*net.TCPAddr).Port
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherPort := other.Addr().(*net.TCPAddr).Port
	workspace := filepath.Join(root, "listener-probe")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(workspace, uid, gid); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "listener-helper"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Args: []string{"/workspace/listener-helper", "-test.run=^TestSandboxListenerHelper$"}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/workspace", "CM_SANDBOX_LISTENER_HELPER=1", "CM_SANDBOX_LISTENER_PORT=" + strconv.Itoa(port)}, Dir: "/workspace", UID: uid, GID: gid, Mounts: []Mount{{Source: workspace, Target: "/workspace"}}}
	args, err := Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
	cmd.WaitDelay = 3 * time.Second
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	tracker, err := Track(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := tracker.Started(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(workspace, "ready")); return err == nil }, "business listener")
	if err := tracker.VerifyListeners(ctx, port, 1); err != nil {
		t.Fatalf("owned listener inspection (requires management SYS_PTRACE): %v", err)
	}
	if err := tracker.VerifyListeners(ctx, otherPort, 1); err == nil {
		t.Fatal("listener in another process namespace accepted")
	}
	if err := tracker.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("probe exit: %v\n%s", err, output.String())
		}
	case <-ctx.Done():
		t.Fatal("listener probe did not stop")
	}
	if err := tracker.WaitStopped(ctx); err != nil {
		t.Fatal(err)
	}
}
