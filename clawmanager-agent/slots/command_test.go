package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestSandboxStarterProvidesCancellableOwnedBusinessCommandRunner(t *testing.T) {
	var starter any = sandboxStarter{cfg: gateway.Config{IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}}}
	runner, ok := starter.(gateway.GatewayCommandRunner)
	if !ok {
		t.Fatal("slot starter has no owned business command runner; automation would fall back to ambient execution")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.RunGatewayCommand(ctx, gateway.GatewayStartSpec{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command was not rejected before launch: %v", err)
	}
	if errors.Is(err, gateway.ErrGatewayStopFailed) {
		t.Fatal("command that never started incorrectly quarantined writer")
	}
}

func TestBusinessCommandSpecSharesOnlyAssignedFilesystemAndExplicitEnvironment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNRECOGNIZED_SUPERVISOR_SECRET", "must-not-inherit")
	s := sandboxStarter{cfg: gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}}}
	uid, gid := 200002, 200002
	if os.Geteuid() != 0 {
		uid, gid = os.Geteuid(), os.Getegid()
	}
	spec := gateway.GatewayStartSpec{InstanceID: 2, UserID: 1, Generation: 1, WorkspacePath: workspace, UID: uid, GID: gid, Command: []string{"openclaw", "automations", "list"}, Env: []string{"HOME=" + filepath.Join(workspace, "home"), "BUSINESS_TOKEN=explicit", "RUNTIME_AGENT_CONTROL_TOKEN=must-drop"}}
	command, groups, err := s.businessCommandSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if command.UID != uid || command.GID != gid || command.Dir != workspace || len(command.Mounts) != 1 || command.Mounts[0].Source != workspace || command.Mounts[0].Target != workspace || len(groups) != 0 {
		t.Fatalf("unexpected command authority: %+v groups=%v", command, groups)
	}
	joined := strings.Join(command.Env, "\n")
	if strings.Contains(joined, "must-") || !strings.Contains(joined, "BUSINESS_TOKEN=explicit") {
		t.Fatalf("incorrect environment: %s", joined)
	}
	spec.WorkspacePath = filepath.Join(root, "openclaw", "user-1", "instance-3")
	if _, err := s.RunGatewayCommand(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "assignment") {
		t.Fatalf("sibling command workspace accepted: %v", err)
	}
}

func TestGatewayCommandOutputSeparatesJSONFromStderrDiagnostics(t *testing.T) {
	// Exercise the runner's capture on a real portable child; the Linux gate also
	// exercises this through RunGatewayCommand and its namespace teardown.
	for _, exit := range []int{0, 7} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			var output boundedCommandOutput
			capture, ok := any(&output).(interface {
				Stdout() io.Writer
				Stderr() io.Writer
				Result(error) ([]byte, error)
			})
			if !ok {
				t.Fatal("owned runner output capture cannot separate JSON stdout from diagnostic stderr")
			}
			cmd := exec.Command("/bin/sh", "-c", `printf '{"jobs":[{"id":"managed-old"}]}';printf 'startup diagnostic trace' >&2;exit "$1"`, "capture-test", strconv.Itoa(exit))
			cmd.Stdout, cmd.Stderr = capture.Stdout(), capture.Stderr()
			err := cmd.Run()
			got, resultErr := capture.Result(err)
			if exit == 0 {
				if resultErr != nil || !json.Valid(got) || string(got) != `{"jobs":[{"id":"managed-old"}]}` {
					t.Fatalf("success JSON contaminated: output=%q error=%v", got, err)
				}
			} else if resultErr == nil || !bytes.Contains(got, []byte("startup diagnostic trace")) || !bytes.Contains(got, []byte("managed-old")) {
				t.Fatalf("failed command lost diagnostics: output=%q error=%v", got, err)
			}
		})
	}
}

func TestGatewayCommandOutputBoundsBothStreamsAndRetainsFailureDiagnostics(t *testing.T) {
	var output boundedCommandOutput
	capture, ok := any(&output).(interface {
		Stdout() io.Writer
		Stderr() io.Writer
		Result(error) ([]byte, error)
	})
	if !ok {
		t.Fatal("owned runner has no split bounded output capture")
	}
	stdout := strings.Repeat("o", 96<<10)
	stderr := strings.Repeat("e", 96<<10) + "\nfinal stderr diagnostic"
	if n, err := io.WriteString(capture.Stdout(), stdout); err != nil || n != len(stdout) {
		t.Fatalf("stdout capture write: %d %v", n, err)
	}
	if n, err := io.WriteString(capture.Stderr(), stderr); err != nil || n != len(stderr) {
		t.Fatalf("stderr capture write: %d %v", n, err)
	}
	if got, err := capture.Result(nil); err == nil || errors.Is(err, gateway.ErrGatewayStopFailed) || len(got) > (64<<10) {
		t.Fatalf("oversized stdout incorrectly succeeded/quarantined: bytes=%d error=%v", len(got), err)
	}
	stopErr := fmt.Errorf("unknown namespace: %w", gateway.ErrGatewayStopFailed)
	got, err := capture.Result(stopErr)
	if len(got) > (64<<10) || !bytes.Contains(got, []byte("final stderr diagnostic")) || !bytes.Contains(got, []byte("oooo")) {
		t.Fatalf("failure tail diagnostics/bound lost: %d %q", len(got), got[:min(len(got), 80)])
	}
	if !errors.Is(err, gateway.ErrGatewayStopFailed) {
		t.Fatal("stop-fence error identity lost")
	}
}

func TestGatewayCommandOutputAllowsStderrOverflowWithValidStdout(t *testing.T) {
	var output boundedCommandOutput
	capture, ok := any(&output).(interface {
		Stdout() io.Writer
		Stderr() io.Writer
		Result(error) ([]byte, error)
	})
	if !ok {
		t.Fatal("owned runner has no split bounded output capture")
	}
	io.WriteString(capture.Stdout(), `{"jobs":[]}`)
	io.WriteString(capture.Stderr(), strings.Repeat("diagnostic trace\n", 16<<10))
	got, err := capture.Result(nil)
	if err != nil || string(got) != `{"jobs":[]}` {
		t.Fatalf("stderr overflow polluted valid stdout: %q %v", got, err)
	}

}
