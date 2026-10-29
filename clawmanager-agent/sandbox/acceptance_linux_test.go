//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run through acceptance-linux.sh: a skip in a regular portable unit run is not
// evidence that the Linux isolation gate passed.
func TestLinuxAcceptance(t *testing.T) {
	if os.Getenv("CM_SANDBOX_ACCEPTANCE") != "1" {
		t.Skip("run sandbox/acceptance-linux.sh on Linux")
	}
	if os.Getuid() == 0 || os.Getgid() == 0 {
		t.Fatal("run acceptance with the intended non-root host UID and GID")
	}
	for _, tool := range []string{"bwrap", "mount", "unshare", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("acceptance requires %s: %v", tool, err)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, team := filepath.Join(root, "instance-a"), filepath.Join(root, "team-a")
	sibling, control := filepath.Join(root, "instance-b"), filepath.Join(root, "controller")
	for _, path := range []string{workspace, team, sibling, control} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(workspace, "own"), filepath.Join(team, "shared"), filepath.Join(sibling, "secret"), filepath.Join(control, "token")} {
		// Public permissions prove isolation, rather than a successful DAC check.
		if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(sibling, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	parentPID := strconv.Itoa(os.Getpid())
	t.Setenv("MANAGEMENT_SECRET", "must-not-be-inherited")
	spec := Spec{UID: os.Getuid(), GID: os.Getgid(), Dir: "/workspace", Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/workspace", "BUSINESS_TOKEN=explicit"}, Mounts: []Mount{{Source: workspace, Target: "/workspace"}, {Source: team, Target: "/team"}}}
	spec.Args = []string{"/bin/sh", "-eux", "-c", `
test "$(cat /workspace/own)" = fixture
test "$(cat /team/shared)" = fixture
printf writable > /workspace/written
printf shared-write > /team/written
test "${MANAGEMENT_SECRET-unset}" = unset
test "$BUSINESS_TOKEN" = explicit
test "$(id -u)" = "$1"
test "$(id -g)" = "$2"
for path in "$3/secret" "$4/token" /workspace/escape/secret "/proc/1/root$3/secret" "/proc/self/root$4/token" /var/run/secrets/kubernetes.io/serviceaccount/token /run/secrets/token; do
  if cat "$path" >/dev/null 2>&1; then printf 'unauthorized read: %s\n' "$path" >&2; exit 1; fi
done
test ! -e "/proc/$5/root"
grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status
test ! -e /sys
test ! -w /proc/sysrq-trigger
test ! -w /proc/sys/kernel/hostname
for path in /proc/kcore /proc/keys /proc/latency_stats /proc/timer_list /proc/timer_stats /proc/sched_debug; do
  if test -e "$path"; then
    test "$(stat -Lc '%t:%T' "$path")" = 1:3
    test "$(head -c 1 "$path" 2>/dev/null | wc -c)" -eq 0
  fi
done
for path in /proc/acpi /proc/asound /proc/scsi; do
  if test -d "$path"; then test -z "$(ls -A "$path")"; test ! -w "$path"; fi
done
for cap in CapInh CapPrm CapEff CapBnd CapAmb; do grep -Eq "^$cap:[[:space:]]+0+$" /proc/self/status; done
mkdir /tmp/remount-probe
if mount -t tmpfs none /tmp/remount-probe 2>/dev/null; then echo unauthorized-mount >&2; exit 1; fi
if unshare -Ur true 2>/dev/null; then echo unauthorized-userns >&2; exit 1; fi
if printf leak > /usr/cm-sandbox-write 2>/dev/null; then echo writable-image >&2; exit 1; fi
`, "acceptance", strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), sibling, control, parentPID}
	cmd := acceptanceCommand(t, spec)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filesystem isolation: %v\n%s", err, output)
	}
	for _, path := range []string{filepath.Join(workspace, "written"), filepath.Join(team, "written")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"normal-exit", "graceful", "kill-monitor"} {
		t.Run(mode, func(t *testing.T) {
			childWorkspace := filepath.Join(root, mode)
			if err := os.Mkdir(childWorkspace, 0755); err != nil {
				t.Fatal(err)
			}
			childSpec := spec
			childSpec.Mounts = []Mount{{Source: childWorkspace, Target: "/workspace"}}
			childSpec.Args = []string{"/bin/sh", "-eu", "-c", `
test ! -e /proc/self/fd/3
test ! -e /proc/self/fd/4
test ! -e /proc/self/fd/5
trap 'printf flushed > /workspace/flushed; exit 0' TERM
setsid /bin/sh -c 'while :; do printf x >> /workspace/writes; sleep 0.05; done' &
printf ready > /workspace/ready
while test ! -f /workspace/release; do sleep 0.05; done
`}
			child := acceptanceCommand(t, childSpec)
			tracker, err := Track(child)
			if err != nil {
				t.Fatal(err)
			}
			defer tracker.Close()
			var output strings.Builder
			child.Stdout, child.Stderr = &output, &output
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill() }()
			startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelStart()
			if err := tracker.Started(startCtx); err != nil {
				t.Fatalf("capture sandbox PID1: %v", err)
			}
			waitFor(t, func() bool { _, err := os.Stat(filepath.Join(childWorkspace, "ready")); return err == nil }, "sandbox readiness")
			waitFor(t, func() bool { data, _ := os.ReadFile(filepath.Join(childWorkspace, "writes")); return len(data) > 0 }, "detached child writes")
			if mode == "kill-monitor" {
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else if mode == "graceful" {
				if err := tracker.Terminate(startCtx); err != nil {
					t.Fatalf("signal primary gateway: %v", err)
				}
			} else if err := os.WriteFile(filepath.Join(childWorkspace, "release"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case err := <-done:
				if mode != "kill-monitor" && err != nil {
					t.Fatalf("sandbox exit: %v\n%s", err, output.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("sandbox did not terminate")
			}
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelStop()
			if err := tracker.WaitStopped(stopCtx); err != nil {
				t.Fatalf("PID namespace teardown: %v", err)
			}
			if mode == "graceful" {
				if _, err := os.Stat(filepath.Join(childWorkspace, "flushed")); err != nil {
					t.Fatal("gateway SIGTERM shutdown hook did not run")
				}
			}
			// Namespace teardown must stop detached writers, including children
			// which escaped the shell's process group using setsid.
			before, _ := os.ReadFile(filepath.Join(childWorkspace, "writes"))
			time.Sleep(150 * time.Millisecond)
			after, _ := os.ReadFile(filepath.Join(childWorkspace, "writes"))
			if string(before) != string(after) {
				t.Fatal("child kept writing after sandbox stopped")
			}
		})
	}
}

func acceptanceCommand(t *testing.T, spec Spec) *exec.Cmd {
	t.Helper()
	args, err := Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	// The test already runs as spec's host UID/GID; production does this with
	// SysProcAttr.Credential. Go closes unrelated FDs; Track only adds its pipes.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// Synthetic outer credential proves --clearenv independently of caller
	// sanitization. No actual management credentials enter the acceptance child.
	cmd.Env = []string{"MANAGEMENT_SECRET=must-not-be-inherited"}
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

func waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("timed out waiting for %s", description))
}
