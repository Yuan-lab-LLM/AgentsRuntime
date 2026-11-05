package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// CM alone decides model inactivity. Every native runtime uses the same
// graceful supervisor; no runtime-specific task/cron inspection is needed.
func TestIdleStopAllRuntimeTypesWithoutNativeHooks(t *testing.T) {
	for _, runtimeType := range []string{"openclaw", "hermes", "opencode", "deepseek-harness"} {
		t.Run(runtimeType, func(t *testing.T) {
			var signals atomic.Int32
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				return ManagedProcess{PID: 1, Stop: func(context.Context) error { t.Error("forceful fallback"); return nil }, GracefulStop: func(context.Context) error { signals.Add(1); return nil }}, nil
			}})
			m.cfg.PodUID, m.cfg.RuntimeType = "pod", runtimeType
			request := isolatedRequest(m, 1, 1, 20000)
			request.AgentType, request.WorkspacePath = runtimeType, filepath.Join(m.cfg.WorkspaceRoot, runtimeType, "user-1", "instance-1")
			created, err := m.CreateGateway(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			awaitIsolatedState(t, m, created.GatewayID, "running")
			if err = m.IdleStopGateway(context.Background(), created.GatewayID, IdleStopRequest{1, 1, "pod"}, nil); err != nil {
				t.Fatal(err)
			}
			if signals.Load() != 1 || len(m.ports.ListUsed()) != 0 {
				t.Fatal("graceful stop did not release the shared slot")
			}
		})
	}
}

func TestIdleStopMissingGracefulStopAndBusyNeverSignal(t *testing.T) {
	for _, mode := range []string{"unsupported", "busy", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			var signals atomic.Int32
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				process := ManagedProcess{PID: 1, Stop: func(context.Context) error { return nil }}
				if mode != "unsupported" {
					process.GracefulStop = func(context.Context) error { signals.Add(1); return nil }
				}
				return process, nil
			}})
			m.cfg.PodUID = "pod"
			created, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
			awaitIsolatedState(t, m, created.GatewayID, "running")
			m.mu.Lock()
			if mode == "busy" {
				m.gateways[created.GatewayID].activeCommands = 1
			}
			if mode == "unknown" {
				m.gateways[created.GatewayID].automationStopFailed = true
			}
			m.mu.Unlock()
			req := IdleStopRequest{InstanceID: 1, Generation: 1, PodUID: "pod"}
			proof, err := m.IdlePreflight(context.Background(), created.GatewayID, req)
			if err != nil || proof.ExecutionState != mode {
				t.Fatalf("proof=%+v err=%v", proof, err)
			}
			fenced := false
			if err = m.IdleStopGateway(context.Background(), created.GatewayID, req, func() error { fenced = true; return nil }); err == nil {
				t.Fatal("unsafe stop accepted")
			}
			if signals.Load() != 0 || fenced {
				t.Fatal("signaled before common supervisor checks")
			}
		})
	}
}

func idleCheckHeld(m *GatewayManager, id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gateways[id].idleChecking
}

func TestIdleStopGracefulTimeoutDoneAndRetryNeverForce(t *testing.T) {
	done := make(chan error)
	var force, graceful atomic.Int32
	var closeDone sync.Once
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 1, Done: done, Stop: func(context.Context) error { force.Add(1); return nil }, GracefulStop: func(context.Context) error {
			if graceful.Add(1) == 1 {
				return context.DeadlineExceeded
			}
			return nil
		}}, nil
	}})
	m.cfg.PodUID = "pod"
	created, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	awaitIsolatedState(t, m, created.GatewayID, "running")
	req := IdleStopRequest{InstanceID: 1, Generation: 1, PodUID: "pod"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.IdleStopGateway(ctx, created.GatewayID, req, func() error { closeDone.Do(func() { close(done) }); return nil })
	if err == nil {
		t.Fatal("unconfirmed first graceful stop accepted")
	}
	if force.Load() != 0 || !idleCheckHeld(m, created.GatewayID) || len(m.ports.ListUsed()) != 1 {
		t.Fatal("timeout lost graceful policy or released reservation/guard")
	}
	if err = m.StopGatewayConfirmed(ctx, created.GatewayID); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatalf("manual fallback=%v", err)
	}
	if _, err = m.CreateGateway(ctx, isolatedRequest(m, 1, 2, 20000)); !errors.Is(err, ErrGatewayStopConflict) {
		t.Fatalf("replacement=%v", err)
	}
	if err = m.IdleStopGateway(ctx, created.GatewayID, req, nil); err != nil {
		t.Fatal(err)
	}
	if force.Load() != 0 || idleCheckHeld(m, created.GatewayID) || len(m.ports.ListUsed()) != 0 {
		t.Fatalf("force=%d guard=%v ports=%v", force.Load(), idleCheckHeld(m, created.GatewayID), m.ports.ListUsed())
	}
}

func TestIdleStopIdentityAndDurableFenceFailureNeverSignal(t *testing.T) {
	var graceful atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 1, Stop: func(context.Context) error { return nil }, GracefulStop: func(context.Context) error { graceful.Add(1); return nil }}, nil
	}})
	m.cfg.PodUID = "pod"
	created, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	awaitIsolatedState(t, m, created.GatewayID, "running")
	for _, req := range []IdleStopRequest{{1, 1, "other"}, {1, 2, "pod"}, {2, 1, "pod"}, {1, 1, "pod"}} {
		if err := m.IdleStopGateway(context.Background(), created.GatewayID, req, func() error { return errors.New("disk unavailable") }); err == nil {
			t.Fatal("invalid identity/fence accepted")
		}
	}
	if graceful.Load() != 0 {
		t.Fatal("signaled before identity and durable fence validated")
	}
}

func TestManagedCommandAdmissionBlocksIdleProofAndFencedCommands(t *testing.T) {
	process := ManagedProcess{PID: 1, VerifyListeners: func(context.Context) error { return nil }}
	starter := managedAutomationTestStarter{run: func(context.Context, GatewayStartSpec) ([]byte, error) {
		t.Fatal("business command escaped idle fence")
		return nil, nil
	}}
	m := NewGatewayManager(Config{}, starter, nil)
	record := &gatewayRecord{process: process, state: GatewayState{State: "running"}, activeCommands: 1}
	m.gateways["gw"] = record
	if _, err := m.acquireIdleGuard(context.Background(), record); !errors.Is(err, ErrGatewayIdleBusy) {
		t.Fatal("native proof acquired with active auxiliary command")
	}
	record.activeCommands = 0
	release, err := m.acquireIdleGuard(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	execute := m.gatewayCommandExecutor(GatewayStartSpec{GatewayID: "gw"}, process, true)
	if _, err = execute(context.Background(), []string{"command"}, nil); !errors.Is(err, ErrGatewayIdleBusy) {
		t.Fatalf("idle-checking command=%v", err)
	}
	release()
	record.idleStop = true
	if _, err = execute(context.Background(), []string{"command"}, nil); !errors.Is(err, ErrGatewayIdleBusy) {
		t.Fatalf("idle-fenced command=%v", err)
	}
}

func TestConfirmedIdleStopAllowsNewerGenerationStart(t *testing.T) {
	var force atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 1, Stop: func(context.Context) error { force.Add(1); return nil }, GracefulStop: func(context.Context) error { return nil }}, nil
	}})
	m.cfg.PodUID = "pod"
	first, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, first.GatewayID, "running")
	if err = m.IdleStopGateway(context.Background(), first.GatewayID, IdleStopRequest{1, 1, "pod"}, nil); err != nil {
		t.Fatal(err)
	}
	next, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 2, 20000))
	if err != nil {
		t.Fatalf("confirmed idle stop permanently blocked restart: %v", err)
	}
	awaitIsolatedState(t, m, next.GatewayID, "running")
	if force.Load() != 0 {
		t.Fatal("restarting confirmed idle stop used forceful cleanup")
	}
	if err = m.IdleStopGateway(context.Background(), next.GatewayID, IdleStopRequest{1, 2, "pod"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestIdleStopCommitsCommonProcessPolicyBeforeCancellation(t *testing.T) {
	for _, withReceipt := range []bool{false, true} {
		t.Run(fmt.Sprint(withReceipt), func(t *testing.T) {
			var inFence, committed atomic.Bool
			var calls atomic.Int32
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				return ManagedProcess{PID: 1, Stop: func(context.Context) error { return nil },
					FenceIdleStop: func(persist func() error) error {
						calls.Add(1)
						inFence.Store(true)
						defer inFence.Store(false)
						if persist != nil {
							if err := persist(); err != nil {
								return err
							}
						}
						committed.Store(true)
						return nil
					},
					GracefulStop: func(context.Context) error {
						if !committed.Load() {
							return errors.New("common process stop policy was not committed")
						}
						return nil
					}}, nil
			}})
			m.cfg.PodUID = "pod"
			created, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
			if err != nil {
				t.Fatal(err)
			}
			awaitIsolatedState(t, m, created.GatewayID, "running")
			var persist func() error
			if withReceipt {
				persist = func() error {
					if !inFence.Load() {
						return errors.New("receipt persisted outside the common stop policy lock")
					}
					return nil
				}
			}
			if err = m.IdleStopGateway(context.Background(), created.GatewayID, IdleStopRequest{1, 1, "pod"}, persist); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || !committed.Load() || len(m.ports.ListUsed()) != 0 {
				t.Fatal("common process fence was bypassed")
			}
		})
	}
}
