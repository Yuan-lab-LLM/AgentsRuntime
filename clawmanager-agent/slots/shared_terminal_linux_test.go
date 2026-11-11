//go:build linux

package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workbench"
)

func TestLinuxWorkbenchSharesPrimaryExecution(t *testing.T) {
	if os.Getenv("CM_WORKBENCH_SANDBOX_ACCEPTANCE") != "1" {
		t.Skip("requires disposable Linux sandbox Pod")
	}
	root, err := os.MkdirTemp("/", "shared-terminal-")
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
	starter := sandboxStarter{cfg: gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}, ProcessStopTimeout: time.Second}}
	spec := gateway.GatewayStartSpec{GatewayID: "gw-2-1", InstanceID: 2, UserID: 1, Generation: 1, RuntimeType: "openclaw", UID: 200002, GID: 200002, WorkspacePath: workspace, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + workspace, "EXPLICIT_BUSINESS=visible"}, Command: []string{"/bin/sh", "-c", "echo shared-run >/tmp/cm-terminal-marker; echo $$ >primary.pid; exec sleep 120"}}
	primary, err := starter.StartGateway(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := primary.Stop(ctx); err != nil {
			t.Errorf("primary stop: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(workspace, "primary.pid")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("primary never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	field := reflect.ValueOf(primary).FieldByName("StartTerminal")
	if !field.IsValid() || field.IsNil() {
		t.Fatal("primary does not provide a terminal attached to its execution")
	}
	launch, ok := field.Interface().(func(context.Context, workbench.TerminalOptions) (workbench.Terminal, error))
	if !ok {
		t.Fatal("invalid primary terminal contract")
	}
	ctxA, cancelA := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelA()
	a, err := launch(ctxA, workbench.TerminalOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer a.IO.Close()
	ctxB, cancelB := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelB()
	b, err := launch(ctxB, workbench.TerminalOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer b.IO.Close()
	doneA := make(chan string, 1)
	go func() { raw, _ := io.ReadAll(a.IO); doneA <- string(raw) }()
	doneB := make(chan string, 1)
	go func() { raw, _ := io.ReadAll(b.IO); doneB <- string(raw) }()
	_, err = io.WriteString(a.IO, "cat /tmp/cm-terminal-marker; ps -p $(cat primary.pid) -o comm=; echo $EXPLICIT_BUSINESS; setsid /bin/sh -c 'trap \"\" HUP TERM; while :; do printf x >> \"$HOME/terminal-writes\"; sleep 0.03; done' >/dev/null 2>&1 & sleep 0.1; exit\n")
	if err != nil {
		t.Fatal(err)
	}
	err = a.Wait()
	out := <-doneA
	if err != nil {
		t.Fatalf("terminal A teardown: %v output=%q", err, out)
	}
	for _, want := range []string{"shared-run", "sleep", "visible"} {
		if !strings.Contains(out, want) {
			t.Fatalf("terminal did not share primary environment: missing %s, output=%q", want, out)
		}
	}
	before, err := os.ReadFile(filepath.Join(workspace, "terminal-writes"))
	if err != nil || len(before) == 0 {
		t.Fatal("detached terminal writer did not run")
	}
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadFile(filepath.Join(workspace, "terminal-writes"))
	if string(before) != string(after) {
		t.Fatal("detached terminal writer survived terminal close")
	}
	select {
	case <-primary.Done:
		t.Fatal("closing terminal killed the primary")
	default:
	}
	_, err = io.WriteString(b.IO, "cat /tmp/cm-terminal-marker; ps -p $(cat primary.pid) -o comm=; echo sibling-alive; exit\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if out := <-doneB; !strings.Contains(out, "sibling-alive") || !strings.Contains(out, "sleep") {
		t.Fatalf("closing A damaged sibling or primary: %q", out)
	}
}

func assertProtectedTerminalSharesGateway(t *testing.T, worker *Worker, id int, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	call := func(op string, value any) any {
		raw, _ := json.Marshal(value)
		result, err := worker.manager.Workbench(ctx, workbench.Request{InstanceID: id, UserID: 1, Generation: 1, GatewayID: fmt.Sprintf("gw-%d-1", id), Operation: op, Data: raw})
		if err != nil {
			t.Fatalf("protected terminal %s: %v", op, err)
		}
		return result
	}
	created := call("terminal.create", workbench.TerminalOptions{Cols: 80, Rows: 24}).(workbench.Session)
	call("terminal.write", map[string]any{"session": created.ID, "input": "readlink /proc/self/ns/net; ps -p 2 -o pid=,comm=; curl -fsS http://127.0.0.1:3000/; echo protected-terminal-done\n"})
	for {
		out := call("terminal.read", map[string]any{"session": created.ID, "cursor": 0}).(workbench.ReadResult)
		text := string(out.Data)
		if strings.Contains(text, namespace) && strings.Contains(text, "protected-terminal-done\r\n") && strings.Contains(text, `"instance":"`+fmt.Sprint(id)+`"`) {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("protected terminal did not enter its gateway environment: %q", text)
		}
		time.Sleep(20 * time.Millisecond)
	}
	call("terminal.close", map[string]any{"session": created.ID})
	state, found := worker.manager.GatewayState(fmt.Sprintf("gw-%d-1", id))
	if !found || state.State != "running" {
		t.Fatal("closing protected terminal stopped its gateway")
	}
}
