package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

func TestContainerStartupFailurePreservesCauseAndWriterFence(t *testing.T) {
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, PrepareReset: func(context.Context, bool) error { return nil }, Stop: func(context.Context) error { return fmt.Errorf("container reset proof required") }}, fmt.Errorf("http_health_failed: diagnostic startup failure")
	}})
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "error")
	m.mu.RLock()
	record := m.gateways[response.GatewayID]
	cause, reserved, pid := record.state.ErrorMessage, record.reserved, record.process.PID
	m.mu.RUnlock()
	if cause != "http_health_failed" {
		t.Fatalf("required container reset masked native startup cause: %s", cause)
	}
	if !reserved || pid != 123 {
		t.Fatal("diagnostic startup error released unconfirmed writer")
	}
}

func TestContainerRunningExitDelegatesRecoveryWithoutReleasingWriter(t *testing.T) {
	done := make(chan error, 1)
	var starts atomic.Int32
	profile := isolatedPolicyProfile{reconciled: make(chan int, 4)}
	m := isolatedTestManager(t, profile, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		starts.Add(1)
		return ManagedProcess{PID: 123, Done: done, PrepareReset: func(context.Context, bool) error { return nil }, Stop: func(context.Context) error { return fmt.Errorf("slot container termination proof is required") }}, nil
	}})
	m.restartDelays = []time.Duration{0}
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "running")
	done <- fmt.Errorf("exit status 1")
	awaitIsolatedState(t, m, response.GatewayID, "error")
	m.mu.RLock()
	record := m.gateways[response.GatewayID]
	cause, reserved, pid, attempt := record.state.ErrorMessage, record.reserved, record.process.PID, record.state.RestartAttempt
	m.mu.RUnlock()
	if cause != "gateway_process_exited" {
		t.Fatalf("required container reset masked running failure: %s", cause)
	}
	if starts.Load() != 1 || !reserved || pid != 123 || attempt != 0 {
		t.Fatalf("native recovery restarted or released unconfirmed writer: starts=%d reserved=%v pid=%d attempt=%d", starts.Load(), reserved, pid, attempt)
	}
}

func TestExplicitContainerForceDeleteTakesOverGracefulFenceAndKeepsReservations(t *testing.T) {
	m, _ := stopFixture(t, nil)
	m.cfg.SandboxProfile = "nfs-rootless-v1"
	id := "gw-12-7"
	record := m.gateways[id]
	record.idleStop = true
	record.activeCommands = 3
	record.reserved = true
	record.finished = make(chan struct{})
	close(record.finished)
	record.cancel = func() {}
	other := &gatewayRecord{state: GatewayState{State: "running", PID: 999}, activeCommands: 1}
	m.gateways["gw-99-1"] = other
	if err := m.PrepareContainerReset(context.Background(), id, false, nil); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatalf("ordinary delete escalated graceful operation: %v", err)
	}
	force, ok := any(m).(interface {
		PrepareContainerForceDelete(context.Context, string) error
	})
	if !ok {
		t.Fatal("explicit force-delete entry point is unavailable")
	}
	// Install through the real process field after the force entry point exists.
	// The force callback must run even though ordinary graceful shutdown is fenced.
	err := force.PrepareContainerForceDelete(context.Background(), id)
	if err == nil {
		t.Fatal("missing force preparation hook claimed cleanup")
	}
	if !record.reserved || record.process.PID != 1234 || m.gateways[id] != record || other.state.State != "running" || other.activeCommands != 1 {
		t.Fatal("force preparation released reservation or changed another gateway")
	}
}

func TestContainerForceDeleteKillsBeforeJoiningGracefulLifecycle(t *testing.T) {
	m, _ := stopFixture(t, nil)
	m.cfg.SandboxProfile = "nfs-rootless-v1"
	record := m.gateways["gw-12-7"]
	record.cancel = func() {}
	record.idleStop = true
	record.reserved = true
	record.finished = make(chan struct{})
	killed := false
	record.process.PrepareForceReset = func(context.Context) error { killed = true; close(record.finished); return nil }
	record.process.PrepareReset = func(context.Context, bool) error { t.Fatal("force delete used TERM/grace timeout"); return nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.PrepareContainerForceDelete(ctx, "gw-12-7"); err != nil || !killed {
		t.Fatalf("force did not preempt graceful lifecycle: %v", err)
	}
	if !record.reserved || record.process.PID == 0 {
		t.Fatal("preparation bypassed whole-container proof")
	}
}

func TestExplicitLegacyForceDeleteNeedsDedicatedStopProof(t *testing.T) {
	m, req := stopFixture(t, nil)
	record := m.gateways[req.GatewayID]
	record.stopOperation = "old-graceful-operation"
	force, ok := any(m).(interface {
		ForceDeleteGateway(context.Context, string) error
	})
	if !ok {
		t.Fatal("explicit legacy force entry point is unavailable")
	}
	if err := force.ForceDeleteGateway(context.Background(), req.GatewayID); !errors.Is(err, ErrGatewayStopUnsupported) {
		t.Fatalf("missing force proof hook was not rejected: %v", err)
	}
	if m.gateways[req.GatewayID] != record {
		t.Fatal("unsupported force released an unconfirmed process")
	}
}

func TestExplicitLegacyForceDeleteRequiresProofBeforeReleasingOnlyTarget(t *testing.T) {
	m, req := stopFixture(t, nil)
	record := m.gateways[req.GatewayID]
	record.stopOperation = "old-graceful-operation"
	other := &gatewayRecord{state: GatewayState{State: "running", PID: 999}}
	m.gateways["gw-99-1"] = other
	calls := 0
	record.process.ForceStop = func(context.Context) error { calls++; return context.DeadlineExceeded }
	if err := m.ForceDeleteGateway(context.Background(), req.GatewayID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if m.gateways[req.GatewayID] != record || record.process.PID == 0 || m.gateways["gw-99-1"] != other || other.state.State != "running" {
		t.Fatal("failed force removed execution or changed another gateway")
	}
	record.process.ForceStop = func(context.Context) error { calls++; return nil }
	if err := m.ForceDeleteGateway(context.Background(), req.GatewayID); err != nil || calls != 2 {
		t.Fatalf("force retry failed: %v calls=%d", err, calls)
	}
	if m.gateways[req.GatewayID] != nil || m.gateways["gw-99-1"] != other {
		t.Fatal("successful force did not release only its target")
	}
	if err := m.ForceDeleteGateway(context.Background(), req.GatewayID); err != nil {
		t.Fatal("force retry was not idempotent", err)
	}
}

type containerResetStarter struct{ isolatedTestStarter }

func (containerResetStarter) PrepareNativeStorage(context.Context, CreateGatewayRequest, int) error {
	return nil
}

func TestContainerForceDeleteDuringStartupUsesOnlyForcePolicy(t *testing.T) {
	entered, publish := make(chan struct{}), make(chan struct{})
	var ordinary, forced atomic.Int32
	cfg := Config{RuntimeType: "hermes", Runtime: isolatedTestProfile{}, WorkspaceRoot: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20000, Capacity: 1, IsolatedLifecycle: true, SandboxProfile: "nfs-rootless-v1"}
	m := NewGatewayManager(cfg, containerResetStarter{isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		close(entered)
		<-publish
		force := func(context.Context) error { forced.Add(1); return nil }
		return ManagedProcess{PID: 42, PrepareForceReset: force, PrepareReset: func(context.Context, bool) error { return nil }, ForceStop: func(ctx context.Context) error { _ = force(ctx); return ErrGatewayStopFailed }, Stop: func(context.Context) error { ordinary.Add(1); return ErrGatewayStopFailed }}, nil
	}}}, NewPortAllocator(func(int) bool { return false }))
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { result <- m.PrepareContainerForceDelete(ctx, response.GatewayID) }()
	// Observe cancellation under the manager lock before releasing process start.
	for {
		m.mu.RLock()
		pending := m.gateways[response.GatewayID].deletePending
		m.mu.RUnlock()
		if pending {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("force delete never fenced startup")
		}
		time.Sleep(time.Millisecond)
	}
	close(publish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if ordinary.Load() != 0 || forced.Load() == 0 {
		t.Fatalf("startup force used ordinary timeout: ordinary=%d forced=%d", ordinary.Load(), forced.Load())
	}
}

func TestContainerOnlyResetRequirementIsPendingNotGenericStopFailure(t *testing.T) {
	for _, realFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(realFailure), func(t *testing.T) {
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				return ManagedProcess{PID: 123, PrepareReset: func(context.Context, bool) error { return nil }, Stop: func(context.Context) error {
					if realFailure {
						return errors.Join(context.DeadlineExceeded, sandbox.ErrContainerResetRequired)
					}
					return errors.Join(nil, sandbox.ErrContainerResetRequired)
				}}, nil
			}})
			response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
			if err != nil {
				t.Fatal(err)
			}
			awaitIsolatedState(t, m, response.GatewayID, "running")
			m.mu.Lock()
			record := m.gateways[response.GatewayID]
			record.cancel()
			m.mu.Unlock()
			awaitIsolatedState(t, m, response.GatewayID, "error")
			m.mu.RLock()
			cause, reserved, pid := record.state.ErrorMessage, record.reserved, record.process.PID
			m.mu.RUnlock()
			want := "container_reset_required"
			if realFailure {
				want = "gateway_stop_failed"
			}
			if cause != want {
				t.Fatalf("cause=%q want=%q", cause, want)
			}
			if !reserved || pid != 123 {
				t.Fatal("pending reset released the old writer before container termination")
			}
		})
	}
}
