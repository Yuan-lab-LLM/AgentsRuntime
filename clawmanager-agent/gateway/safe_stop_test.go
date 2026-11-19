package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func stopFixture(t *testing.T, stop func(context.Context) error) (*GatewayManager, ConfirmStopRequest) {
	t.Helper()
	cfg := Config{RuntimeType: "opencode", PodUID: "pod-uid", WorkspaceRoot: t.TempDir(), GatewayPortStart: 31000, GatewayPortEnd: 31000, Capacity: 1}
	m := NewGatewayManager(cfg, nil, NewPortAllocator(func(int) bool { return false }))
	generation := 7
	req := ConfirmStopRequest{InstanceID: 12, UserID: 4, Generation: &generation, GatewayID: "gw-12-7", PodUID: cfg.PodUID,
		AgentEpoch: m.stopEpoch, OperationID: "0123456789abcdef0123456789abcdef", TimeoutSeconds: 1}
	port, err := m.ports.Reserve(12, 7, PortRange{Start: 31000, End: 31000})
	if err != nil {
		t.Fatal(err)
	}
	m.gateways[req.GatewayID] = &gatewayRecord{state: GatewayState{InstanceID: 12, UserID: 4, Generation: 7, GatewayID: req.GatewayID, State: "running", Port: port},
		process: ManagedProcess{PID: 1234, GracefulStop: stop, Stop: func(context.Context) error { t.Error("force/legacy stop called"); return nil }}}
	return m, req
}

func createDuringStop(m *GatewayManager, generation int) error {
	_, err := m.CreateGateway(context.Background(), CreateGatewayRequest{InstanceID: 12, UserID: 4, Generation: generation, AgentType: "opencode",
		WorkspacePath: filepath.Join(m.cfg.WorkspaceRoot, "opencode", "user-4", "instance-12")})
	return err
}

func TestConfirmedStopChecksExpectedPIDBeforeSignal(t *testing.T) {
	calls := 0
	m, req := stopFixture(t, func(context.Context) error { calls++; return nil })
	if !m.StopCapabilities().ExpectedPIDGuard {
		t.Fatal("missing capability")
	}
	req.ExpectedPID = -1
	if req.Valid() {
		t.Fatal("negative PID accepted")
	}
	req.ExpectedPID = 9999
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopConflict) || calls != 0 {
		t.Fatal("wrong process stopped", err, calls)
	}
	if m.gateways[req.GatewayID].stopOperation != "" {
		t.Fatal("failed validation mutated operation")
	}
	req.ExpectedPID = 1234
	if _, err := m.ConfirmStop(context.Background(), req); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	req.ExpectedPID = 9999
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopConflict) || calls != 1 {
		t.Fatal("retry retargeted", err, calls)
	}
}

func TestConfirmedStopRetainsFenceAndReservation(t *testing.T) {
	calls := 0
	m, req := stopFixture(t, func(context.Context) error { calls++; return nil })
	for range 2 {
		receipt, err := m.ConfirmStop(context.Background(), req)
		if err != nil || receipt == nil || !receipt.Confirmed || receipt.OperationID != req.OperationID || receipt.ConfirmationScope != "process_group" {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if calls != 2 || m.UsedSlots() != 1 {
		t.Fatal("lost stop callback/reserved slot")
	}
	for _, generation := range []int{7, 8} {
		if !errors.Is(createDuringStop(m, generation), ErrGatewayStopConflict) {
			t.Fatal("replacement allowed")
		}
	}
	if !errors.Is(m.DeleteGateway(context.Background(), req.GatewayID), ErrGatewayStopConflict) {
		t.Fatal("DELETE bypassed fence")
	}
	if _, err := m.ports.Reserve(99, 1, PortRange{Start: 31000, End: 31000}); err == nil {
		t.Fatal("released port")
	}
}

func TestConfirmedStopFailureRetryAndConcurrentStop(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	m, req := stopFixture(t, func(context.Context) error { close(entered); <-finish; return context.DeadlineExceeded })
	result := make(chan error, 1)
	go func() { _, err := m.ConfirmStop(context.Background(), req); result <- err }()
	<-entered
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatal("concurrent stop accepted")
	}
	if !errors.Is(createDuringStop(m, 8), ErrGatewayStopConflict) {
		t.Fatal("concurrent create accepted")
	}
	close(finish)
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if m.UsedSlots() != 1 {
		t.Fatal("failure released reservation")
	}
	record := m.gateways[req.GatewayID]
	if record.stopConfirmed || record.process.PID == 0 {
		t.Fatal("failure was certified/forgotten")
	}
	other := req
	other.OperationID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := m.ConfirmStop(context.Background(), other); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatal("operation takeover")
	}
	record.process.GracefulStop = func(context.Context) error { return nil }
	if _, err := m.ConfirmStop(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmedStopIdentityAndUnsupportedLeaveProcessAlone(t *testing.T) {
	for _, change := range []func(*ConfirmStopRequest){
		func(r *ConfirmStopRequest) { r.PodUID = "old-pod" }, func(r *ConfirmStopRequest) { r.AgentEpoch = "old-agent" },
		func(r *ConfirmStopRequest) { r.UserID++ }, func(r *ConfirmStopRequest) { r.Generation = nil },
		func(r *ConfirmStopRequest) { r.GatewayID = "gw-12-8" }, func(r *ConfirmStopRequest) { r.OperationID = "bad" },
		func(r *ConfirmStopRequest) { r.TimeoutSeconds = 21 },
	} {
		m, req := stopFixture(t, func(context.Context) error { t.Fatal("invalid stop signalled process"); return nil })
		change(&req)
		if _, err := m.ConfirmStop(context.Background(), req); err == nil {
			t.Fatal("accepted invalid identity")
		}
		if m.gateways["gw-12-7"].stopOperation != "" {
			t.Fatal("invalid request fenced healthy instance")
		}
	}
	m, req := stopFixture(t, nil)
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopUnsupported) {
		t.Fatal(err)
	}
	if m.gateways[req.GatewayID].stopOperation != "" {
		t.Fatal("legacy instance fenced")
	}
}

func TestConfirmedStopRejectsStartingAndCancelledRequest(t *testing.T) {
	m, req := stopFixture(t, func(context.Context) error { t.Fatal("unexpected stop"); return nil })
	m.gateways[req.GatewayID].state.State = "starting"
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatal(err)
	}
	if err := m.DeleteGateway(context.Background(), req.GatewayID); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatal(err)
	}
	m.gateways[req.GatewayID].state.State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ConfirmStop(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestProcessWatcherCannotRemoveStopFenceOrReplacement(t *testing.T) {
	m, req := stopFixture(t, func(context.Context) error { return nil })
	done := make(chan error, 1)
	m.gateways[req.GatewayID].process.Done = done
	if _, err := m.ConfirmStop(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	done <- nil
	m.watchGatewayProcess(req.GatewayID, 1234, done)
	if m.UsedSlots() != 1 || m.gateways[req.GatewayID].process.PID != 1234 {
		t.Fatal("watcher lost fenced process")
	}
	other := make(chan error, 1)
	other <- nil
	m.gateways[req.GatewayID].stopOperation = ""
	m.gateways[req.GatewayID].state.State = "running"
	m.watchGatewayProcess(req.GatewayID, 1234, other)
	if m.gateways[req.GatewayID].state.State != "running" {
		t.Fatal("old watcher changed replacement")
	}
}

func TestLegacyDeleteReturnsStopFailureAndRetainsProcess(t *testing.T) {
	m, req := stopFixture(t, nil)
	record := m.gateways[req.GatewayID]
	record.process.Stop = func(context.Context) error { return context.DeadlineExceeded }
	if err := m.DeleteGateway(context.Background(), req.GatewayID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if m.UsedSlots() != 1 || m.gateways[req.GatewayID] != record {
		t.Fatal("failed delete removed process")
	}
	if !errors.Is(createDuringStop(m, 8), ErrGatewayStopConflict) {
		t.Fatal("replacement after failed delete")
	}
	record.process.Stop = func(context.Context) error { return nil }
	if err := m.DeleteGateway(context.Background(), req.GatewayID); err != nil {
		t.Fatal(err)
	}
	if m.UsedSlots() != 0 || m.gateways[req.GatewayID] != nil {
		t.Fatal("successful delete retained record")
	}
	if err := m.DeleteGateway(context.Background(), req.GatewayID); err != nil {
		t.Fatal("delete not idempotent")
	}
}

func TestConfirmedStopDoesNotBypassIsolatedLifecycle(t *testing.T) {
	m, req := stopFixture(t, func(context.Context) error {
		t.Fatal("process-group stop bypassed native lifecycle")
		return nil
	})
	m.cfg.IsolatedLifecycle = true
	if m.StopCapabilities().GracefulStop {
		t.Fatal("native lifecycle advertised legacy process-group stop")
	}
	if _, err := m.ConfirmStop(context.Background(), req); !errors.Is(err, ErrGatewayStopUnsupported) {
		t.Fatal(err)
	}
	if m.gateways[req.GatewayID].stopOperation != "" || m.gateways[req.GatewayID].state.State != "running" {
		t.Fatal("unsupported stop mutated native lifecycle")
	}
}
