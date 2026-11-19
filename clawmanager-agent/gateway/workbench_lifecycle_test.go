package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"io"
	"testing"
	"time"
)

func TestWorkbenchStopCancelsAndJoinsGitBeforeRelease(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	primaryStopped := make(chan struct{}, 1)
	s := &workbenchStarter{isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 9, VerifyListeners: func(context.Context) error { return nil }, Stop: func(context.Context) error { primaryStopped <- struct{}{}; return nil }}, nil
	}}}
	s.run = func(ctx context.Context, _ GatewayStartSpec) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}
	m := isolatedTestManager(t, isolatedTestProfile{}, s.isolatedTestStarter)
	m.starter = s
	created, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	awaitIsolatedState(t, m, created.GatewayID, "running")
	req := workbench.Request{InstanceID: 1, UserID: 1, Generation: 1, GatewayID: created.GatewayID, Operation: "git", Data: json.RawMessage(`{"operation":"projects"}`)}
	commandDone := make(chan error, 1)
	go func() { _, err := m.Workbench(context.Background(), req); commandDone <- err }()
	<-entered
	stopDone := make(chan error, 1)
	go func() { stopDone <- m.StopGatewayConfirmed(context.Background(), created.GatewayID) }()
	awaitIsolatedState(t, m, created.GatewayID, "stopping")
	if _, err := m.Workbench(context.Background(), req); !errors.Is(err, ErrGatewayIdleBusy) {
		t.Fatalf("admitted during stop %v", err)
	}
	select {
	case <-primaryStopped:
		t.Fatal("primary stopped before helper joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-commandDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
}

type testTerminalReader struct{ *io.PipeReader }

func (testTerminalReader) Write(b []byte) (int, error) { return len(b), nil }

type terminalWorkbenchStarter struct {
	*workbenchStarter
	closed chan struct{}
}

func (s *terminalWorkbenchStarter) StartGatewayTerminal(ctx context.Context, _ GatewayStartSpec, _ workbench.TerminalOptions) (workbench.Terminal, error) {
	r, w := io.Pipe()
	go func() { <-ctx.Done(); w.Close(); close(s.closed) }()
	return workbench.Terminal{IO: testTerminalReader{r}, Wait: func() error { <-s.closed; return nil }}, nil
}
func TestWorkbenchTerminalDoesNotHoldIdleGuard(t *testing.T) {
	base := &workbenchStarter{isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 10, VerifyListeners: func(context.Context) error { return nil }, Stop: func(context.Context) error { return nil }}, nil
	}}}
	s := &terminalWorkbenchStarter{workbenchStarter: base, closed: make(chan struct{})}
	m := isolatedTestManager(t, isolatedTestProfile{}, base.isolatedTestStarter)
	m.starter = s
	created, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	awaitIsolatedState(t, m, created.GatewayID, "running")
	req := workbench.Request{InstanceID: 1, UserID: 1, Generation: 1, GatewayID: created.GatewayID, Operation: "terminal.create", Data: json.RawMessage(`{}`)}
	if _, err := m.Workbench(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	record := m.gateways[created.GatewayID]
	release, err := m.acquireIdleGuard(context.Background(), record)
	if err != nil {
		t.Fatal("open PTY blocks idle", err)
	}
	release()
	if err := m.StopGatewayConfirmed(context.Background(), created.GatewayID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.closed:
	default:
		t.Fatal("stop left terminal alive")
	}
}
