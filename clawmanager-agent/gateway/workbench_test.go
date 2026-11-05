package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"net"
	"sync/atomic"
	"testing"
)

type workbenchStarter struct {
	isolatedTestStarter
	calls atomic.Int32
	run   func(context.Context, GatewayStartSpec) ([]byte, error)
}

func (s *workbenchStarter) RunGatewayCommand(ctx context.Context, spec GatewayStartSpec) ([]byte, error) {
	s.calls.Add(1)
	return s.run(ctx, spec)
}
func TestWorkbenchScopeAndBusinessExecution(t *testing.T) {
	s := &workbenchStarter{isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 42, Stop: func(context.Context) error { return nil }, VerifyListeners: func(context.Context) error { return nil }}, nil
	}}}
	s.run = func(_ context.Context, spec GatewayStartSpec) ([]byte, error) {
		if len(spec.Command) != 2 || spec.Command[1] != "managed-workbench-git" || spec.UID != 1000 {
			t.Fatalf("unsafe command: %+v", spec)
		}
		var p struct {
			Root string `json:"root"`
		}
		_ = json.Unmarshal(spec.Stdin, &p)
		if p.Root != spec.WorkspacePath {
			t.Fatal("untrusted workspace")
		}
		return []byte(`{"result":{"projects":[]}}`), nil
	}
	m := isolatedTestManager(t, isolatedTestProfile{}, s.isolatedTestStarter)
	m.starter = s
	created, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, created.GatewayID, "running")
	req := workbench.Request{InstanceID: 1, UserID: 1, Generation: 1, GatewayID: created.GatewayID, Operation: "git", Data: json.RawMessage(`{"operation":"projects"}`)}
	for _, field := range []string{"generation", "instance", "user", "gateway"} {
		bad := req
		switch field {
		case "generation":
			bad.Generation++
		case "instance":
			bad.InstanceID++
		case "user":
			bad.UserID++
		case "gateway":
			bad.GatewayID = "other"
		}
		for _, op := range []string{"git", "terminal.create"} {
			bad.Operation = op
			if _, err = m.Workbench(context.Background(), bad); !errors.Is(err, ErrStaleGeneration) {
				t.Fatalf("%s/%s: %v", field, op, err)
			}
		}
	}
	if s.calls.Load() != 0 {
		t.Fatal("mismatch executed")
	}
	if _, err = m.Workbench(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s.calls.Load() != 1 {
		t.Fatal("business executor bypassed")
	}
}

func TestWorkbenchPreviewLifetimeDoesNotPreventIdle(t *testing.T) {
	s := &previewTestStarter{isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 42, Stop: func(context.Context) error { return nil }, VerifyListeners: func(context.Context) error { return nil }}, nil
	}}}
	m := isolatedTestManager(t, isolatedTestProfile{}, s.isolatedTestStarter)
	m.starter = s
	created, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, created.GatewayID, "running")
	req := workbench.Request{InstanceID: 1, UserID: 1, Generation: 1, GatewayID: created.GatewayID}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.WorkbenchPreviewWithAdmission(context.Background(), req, nil, func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) error {
			close(entered)
			<-ctx.Done()
			return nil
		})
	}()
	<-entered
	m.mu.Lock()
	record := m.gateways[created.GatewayID]
	active := record.activeCommands
	m.mu.Unlock()
	if active != 0 {
		t.Fatalf("preview blocked idle: %d", active)
	}
	if err = m.closeWorkbench(record); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	req.Generation++
	if err = m.WorkbenchPreviewWithAdmission(context.Background(), req, nil, nil); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale: %v", err)
	}
}

type previewTestStarter struct{ isolatedTestStarter }

func (*previewTestStarter) GatewayPreviewAvailable(GatewayStartSpec) bool { return true }
func (*previewTestStarter) DialGatewayPreview(context.Context, GatewayStartSpec, string, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
