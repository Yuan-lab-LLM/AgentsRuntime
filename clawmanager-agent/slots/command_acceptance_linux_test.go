//go:build linux

package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// Execute as the actual root slot supervisor in its deployment security context.
// A normal portable test skip is not evidence that this Linux gate passed.
func TestLinuxSlotCommandAcceptance(t *testing.T) {
	if os.Getenv("CM_SLOT_COMMAND_ACCEPTANCE") != "1" {
		t.Skip("set CM_SLOT_COMMAND_ACCEPTANCE=1 in the Linux slot supervisor security context")
	}
	if os.Getuid() != 0 {
		t.Fatal("acceptance requires the actual root supervisor identity")
	}
	for _, tool := range []string{"bwrap", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	uid, gid := 200002, 200002
	for key, value := range map[string]*int{"CM_SANDBOX_UID": &uid, "CM_SANDBOX_GID": &gid} {
		if raw := os.Getenv(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				t.Fatalf("invalid %s", key)
			}
			*value = n
		}
	}
	root, err := os.MkdirTemp("/tmp", "cm-slot-command-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(root, "workspaces")
	sibling, control := filepath.Join(pool, "openclaw", "user-1", "instance-3"), filepath.Join(root, "controller")
	for _, dir := range []string{sibling, control} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("public-mode-still-hidden"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("UNRECOGNIZED_SUPERVISOR_SECRET", "must-not-be-inherited")
	for index, mode := range []string{"graceful", "forced"} {
		t.Run(mode, func(t *testing.T) {
			instance := index + 7
			workspace := filepath.Join(pool, "openclaw", "user-1", fmt.Sprintf("instance-%d", instance))
			home := filepath.Join(workspace, "home")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{workspace, home} {
				if err := os.Chown(dir, uid, gid); err != nil {
					t.Fatal(err)
				}
			}
			cfg := gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: pool, IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/local/bin:/usr/bin:/bin"}, ProcessStopTimeout: 300 * time.Millisecond}
			script := `
set -eu
test "$(id -u)" = "$1"
test "$(id -g)" = "$2"
test "$HOME" = "$3/home"
test "${UNRECOGNIZED_SUPERVISOR_SECRET-unset}" = unset
test "$BUSINESS_TOKEN" = explicit
for path in "$4/secret" "$5/secret" "$3/escape/secret" "/proc/$6/root$5/secret" /var/run/secrets/kubernetes.io/serviceaccount/token /run/clawmanager-slots; do
 if test -e "$path"; then printf 'unauthorized visibility: %s\n' "$path" >&2;exit 1;fi
done
grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status
grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status
for fd in 3 4 5; do test ! -e "/proc/self/fd/$fd";done
if test "$7" = forced;then trap '' TERM;else trap 'exit 0' TERM;fi
setsid /bin/sh -c 'trap "" TERM;while :;do printf x >> "$1/writes";sleep 0.05;done' child "$3" &
printf boundary-ok
printf ready > "$3/ready"
while :;do sleep 0.05;done
`
			if err := os.Symlink(sibling, filepath.Join(workspace, "escape")); err != nil {
				t.Fatal(err)
			}
			spec := gateway.GatewayStartSpec{InstanceID: instance, UserID: 1, Generation: 1, WorkspacePath: workspace, UID: uid, GID: gid,
				Env:     []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "BUSINESS_TOKEN=explicit"},
				Command: []string{"/bin/sh", "-c", script, "owned-command", strconv.Itoa(uid), strconv.Itoa(gid), workspace, sibling, control, strconv.Itoa(os.Getpid()), mode}}
			// A successful official CLI may log startup diagnostics on stderr while
			// stdout remains machine-readable JSON. Error output must retain both.
			for _, exit := range []int{0, 7} {
				quick := spec
				quick.Command = []string{"/bin/sh", "-c", `printf '{"jobs":[{"id":"managed-old"}]}';printf 'startup diagnostic trace' >&2;exit "$1"`, "quick-command", strconv.Itoa(exit)}
				quickCtx, cancelQuick := context.WithTimeout(context.Background(), 10*time.Second)
				output, err := (sandboxStarter{cfg: cfg}).RunGatewayCommand(quickCtx, quick)
				cancelQuick()
				if exit == 0 {
					if err != nil || !json.Valid(output) || string(output) != `{"jobs":[{"id":"managed-old"}]}` {
						t.Fatalf("successful CLI JSON contaminated by stderr: output=%q error=%v", output, err)
					}
				} else if err == nil || errors.Is(err, gateway.ErrGatewayStopFailed) || !strings.Contains(string(output), "startup diagnostic trace") || !strings.Contains(string(output), "managed-old") {
					t.Fatalf("failed CLI diagnostics or namespace proof lost: output=%q error=%v", output, err)
				}
			}
			for _, overflow := range []string{"stdout", "stderr"} {
				quick := spec
				script := `printf '{"jobs":[]}';head -c 65537 /dev/zero >&2;printf 'final stderr diagnostic' >&2`
				if overflow == "stdout" {
					script = `head -c 65537 /dev/zero;printf 'final stderr diagnostic' >&2`
				}
				quick.Command = []string{"/bin/sh", "-c", script}
				quickCtx, cancelQuick := context.WithTimeout(context.Background(), 10*time.Second)
				output, err := (sandboxStarter{cfg: cfg}).RunGatewayCommand(quickCtx, quick)
				cancelQuick()
				if overflow == "stdout" {
					if err == nil || errors.Is(err, gateway.ErrGatewayStopFailed) || !strings.Contains(err.Error(), "stdout exceeded") || len(output) > (64<<10) || !strings.Contains(string(output), "final stderr diagnostic") {
						t.Fatalf("stdout overflow did not fail safely: bytes=%d error=%v", len(output), err)
					}
				} else if err != nil || string(output) != `{"jobs":[]}` {
					t.Fatalf("stderr overflow contaminated stdout: output=%q error=%v", output, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				output []byte
				err    error
			}
			finished := make(chan result, 1)
			go func() {
				out, err := (sandboxStarter{cfg: cfg}).RunGatewayCommand(ctx, spec)
				finished <- result{out, err}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(workspace, "ready")); err == nil {
					break
				}
				select {
				case got := <-finished:
					t.Fatalf("command failed before readiness: %s %v", got.output, got.err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("business command never became ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Require the descendant writer to have actually run before testing teardown.
			for {
				if info, err := os.Stat(filepath.Join(workspace, "writes")); err == nil && info.Size() > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("background writer never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			select {
			case got := <-finished:
				if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, gateway.ErrGatewayStopFailed) || !strings.Contains(string(got.output), "boundary-ok") {
					t.Fatalf("command stop was not confirmed: output=%s error=%v", got.output, got.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("command namespace did not drain")
			}
			before, err := os.ReadFile(filepath.Join(workspace, "writes"))
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			after, err := os.ReadFile(filepath.Join(workspace, "writes"))
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatal("detached descendant still writes after command return")
			}
		})
	}
}
