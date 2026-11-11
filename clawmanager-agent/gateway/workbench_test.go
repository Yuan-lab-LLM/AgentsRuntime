package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

type migratingWorkbenchStarter struct {
	*workbenchStarter
	terminalCancelled chan struct{}
	terminalRelease   <-chan struct{}
}

func (s *migratingWorkbenchStarter) StartGatewayTerminal(ctx context.Context, _ GatewayStartSpec, _ workbench.TerminalOptions) (workbench.Terminal, error) {
	reader, writer := io.Pipe()
	return workbench.Terminal{IO: testTerminalReader{reader}, Wait: func() error {
		<-ctx.Done()
		close(s.terminalCancelled)
		<-s.terminalRelease
		writer.Close()
		return nil
	}}, nil
}

func TestWorkbenchMigrationJoinsGitAndTerminalBeforeSourceReuse(t *testing.T) {
	gitEntered, gitCancelled, gitRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	terminalCancelled, terminalRelease := make(chan struct{}), make(chan struct{})
	primaryStopped, successorStarted := make(chan struct{}), make(chan struct{})
	var releaseGit, releaseTerminal sync.Once
	defer releaseGit.Do(func() { close(gitRelease) })
	defer releaseTerminal.Do(func() { close(terminalRelease) })
	base := &workbenchStarter{isolatedTestStarter: isolatedTestStarter{start: func(spec GatewayStartSpec) (ManagedProcess, error) {
		if spec.Generation == 2 {
			close(successorStarted)
		}
		return ManagedProcess{PID: spec.Generation, VerifyListeners: func(context.Context) error { return nil }, Stop: func(context.Context) error {
			if spec.Generation == 1 {
				close(primaryStopped)
			}
			return nil
		}}, nil
	}}}
	base.run = func(ctx context.Context, _ GatewayStartSpec) ([]byte, error) {
		close(gitEntered)
		<-ctx.Done()
		close(gitCancelled)
		<-gitRelease
		return nil, ctx.Err()
	}
	starter := &migratingWorkbenchStarter{workbenchStarter: base, terminalCancelled: terminalCancelled, terminalRelease: terminalRelease}
	manager := isolatedTestManager(t, isolatedTestProfile{}, base.isolatedTestStarter)
	manager.starter = starter
	first, err := manager.CreateGateway(context.Background(), isolatedRequest(manager, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, manager, first.GatewayID, "running")
	request := workbench.Request{InstanceID: 1, UserID: 1, Generation: 1, GatewayID: first.GatewayID, Operation: "terminal.create", Data: json.RawMessage(`{}`)}
	if _, err = manager.Workbench(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Operation = "git"
	request.Data = json.RawMessage(`{"operation":"projects"}`)
	gitDone := make(chan error, 1)
	go func() { _, err := manager.Workbench(context.Background(), request); gitDone <- err }()
	wait := func(c <-chan struct{}, name string) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s not observed", name)
		}
	}
	wait(gitEntered, "Git start")
	manager.mu.Lock()
	old := manager.gateways[first.GatewayID]
	manager.mu.Unlock()
	second, err := manager.CreateGateway(context.Background(), isolatedRequest(manager, 1, 2, 20000))
	if err != nil {
		t.Fatal(err)
	}
	wait(gitCancelled, "Git cancellation")
	wait(terminalCancelled, "terminal cancellation")
	assertFenced := func() {
		t.Helper()
		for name, c := range map[string]<-chan struct{}{"primary stop": primaryStopped, "old completion": old.finished, "successor start": successorStarted} {
			select {
			case <-c:
				t.Fatalf("%s preceded auxiliary joins", name)
			default:
			}
		}
		if _, err := manager.Workbench(context.Background(), request); err == nil {
			t.Fatal("old work admitted during migration")
		}
		next := request
		next.GatewayID = second.GatewayID
		next.Generation = 2
		if _, err := manager.Workbench(context.Background(), next); err == nil {
			t.Fatal("replacement admitted while predecessor work remained")
		}
		if _, err := manager.CreateGateway(context.Background(), isolatedRequest(manager, 2, 1, 20000)); !errors.Is(err, ErrNoFreePort) {
			t.Fatalf("source port reused: %v", err)
		}
	}
	assertFenced()
	releaseGit.Do(func() { close(gitRelease) })
	select {
	case err := <-gitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Git failed to join")
	}
	assertFenced()
	releaseTerminal.Do(func() { close(terminalRelease) })
	wait(old.finished, "old completion")
	wait(primaryStopped, "primary stop")
	wait(successorStarted, "successor start")
	awaitIsolatedState(t, manager, second.GatewayID, "running")
	if _, err = manager.Workbench(context.Background(), request); err == nil {
		t.Fatal("stale-generation work admitted after migration")
	}
	if _, err = manager.CreateGateway(context.Background(), isolatedRequest(manager, 1, 1, 20000)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation replaced successor: %v", err)
	}
}
