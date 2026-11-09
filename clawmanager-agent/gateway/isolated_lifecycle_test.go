package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type isolatedTestProfile struct {
	openClawCompatProfile
	prepare func(CreateGatewayRequest) error
}

func (isolatedTestProfile) IsolatedGatewayLifecycle() bool { return true }
func (p isolatedTestProfile) PrepareWorkspace(_ Config, req CreateGatewayRequest, _ string) error {
	if p.prepare != nil {
		return p.prepare(req)
	}
	return nil
}
func (isolatedTestProfile) WriteGatewayConfig(Config, CreateGatewayRequest, string, int) error {
	return nil
}

type isolatedTestStarter struct {
	start func(GatewayStartSpec) (ManagedProcess, error)
}

func (s isolatedTestStarter) StartGateway(_ context.Context, spec GatewayStartSpec) (ManagedProcess, error) {
	return s.start(spec)
}
func isolatedTestManager(t *testing.T, profile RuntimeProfile, starter isolatedTestStarter) *GatewayManager {
	t.Helper()
	cfg := Config{RuntimeType: "hermes", Runtime: profile, WorkspaceRoot: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20299, GatewayPortBlockSize: 1, Capacity: 100, ProcessStopTimeout: time.Second}
	m := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}
func isolatedRequest(m *GatewayManager, id, gen, port int) CreateGatewayRequest {
	return CreateGatewayRequest{InstanceID: id, UserID: 1, Generation: gen, AgentType: "hermes", GatewayPort: port, UID: 1000, GID: 1000, WorkspacePath: filepath.Join(m.cfg.WorkspaceRoot, "hermes", "user-1", fmt.Sprintf("instance-%d", id))}
}
func awaitIsolatedState(t *testing.T, m *GatewayManager, id, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.GatewayStates() {
			if s.GatewayID == id && s.State == state {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s never reached %s: %#v", id, state, m.GatewayStates())
}
func TestIsolatedReplacementWaitsForStopAndPreservesPort(t *testing.T) {
	stopStarted, stopRelease, secondStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(stopRelease) })
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(spec GatewayStartSpec) (ManagedProcess, error) {
		if spec.Generation == 2 {
			close(secondStarted)
		}
		return ManagedProcess{PID: spec.Generation, Stop: func(context.Context) error {
			if spec.Generation == 1 {
				close(stopStarted)
				<-stopRelease
			}
			return nil
		}}, nil
	}})
	first, _ := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	awaitIsolatedState(t, m, first.GatewayID, "running")
	start := time.Now()
	second, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 2, 20000))
	if err != nil || second.Status != "starting" || time.Since(start) > time.Second {
		t.Fatalf("replacement=%#v,%v", second, err)
	}
	<-stopStarted
	select {
	case <-secondStarted:
		t.Fatal("successor started before predecessor stopped")
	default:
	}
	if _, err = m.CreateGateway(context.Background(), isolatedRequest(m, 2, 1, 20000)); !errors.Is(err, ErrNoFreePort) {
		t.Fatalf("port reused during stop: %v", err)
	}
	release.Do(func() { close(stopRelease) })
	awaitIsolatedState(t, m, second.GatewayID, "running")
	if _, err = m.CreateGateway(context.Background(), isolatedRequest(m, 1, 0, 20001)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale create = %v", err)
	}
	if err = m.DeleteGateway(context.Background(), second.GatewayID); err != nil {
		t.Fatal(err)
	}
	if len(m.ports.ListUsed()) != 0 {
		t.Fatal("delete leaked port")
	}
}
func TestIsolatedDeleteCancelsQueuedStartAndWaitsForWriter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var starts atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{prepare: func(CreateGatewayRequest) error { close(entered); <-release; return nil }}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) { starts.Add(1); return ManagedProcess{}, nil }})
	resp, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	deleted := make(chan error, 1)
	go func() { deleted <- m.DeleteGateway(context.Background(), resp.GatewayID) }()
	m.mu.RLock()
	record := m.gateways[resp.GatewayID]
	m.mu.RUnlock()
	// Cancel explicitly under lock to make the subsequent assertion deterministic.
	m.mu.Lock()
	record.cancel()
	m.mu.Unlock()
	select {
	case <-deleted:
		t.Fatal("delete returned during workspace writes")
	default:
	}
	close(release)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 || len(m.ports.ListUsed()) != 0 {
		t.Fatal("cancelled gateway started or leaked reservation")
	}
}
func TestIsolatedConcurrentCapacityAndIdempotency(t *testing.T) {
	var starts atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: int(starts.Add(1)), Stop: func(context.Context) error { return nil }}, nil
	}})
	var wg sync.WaitGroup
	for id := 1; id <= 100; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := isolatedRequest(m, id, 1, 20000+id-1)
			for range 2 {
				if _, err := m.CreateGateway(context.Background(), req); err != nil {
					t.Errorf("create %d: %v", id, err)
				}
			}
		}(id)
	}
	wg.Wait()
	for id := 1; id <= 100; id++ {
		awaitIsolatedState(t, m, gatewayID(id, 1), "running")
	}
	if starts.Load() != 100 || m.UsedSlots() != 100 || len(m.ports.ListUsed()) != 100 {
		t.Fatal("concurrent create not isolated/idempotent")
	}
	m.SetDraining(true)
	if _, err := m.CreateGateway(context.Background(), isolatedRequest(m, 101, 1, 20100)); !errors.Is(err, ErrDraining) {
		t.Fatal(err)
	}
}
func TestIsolatedErrorsNeverReportRawSecrets(t *testing.T) {
	m := isolatedTestManager(t, isolatedTestProfile{prepare: func(CreateGatewayRequest) error { return fmt.Errorf("dashboard_auth_failed: password=super-secret") }}, isolatedTestStarter{})
	resp, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, resp.GatewayID, "error")
	state := m.GatewayStates()[0]
	if state.ErrorMessage != "dashboard_auth_failed" || strings.Contains(state.ErrorMessage, "secret") {
		t.Fatal(state.ErrorMessage)
	}
	if m.UsedSlots() != 0 || len(m.ports.ListUsed()) != 0 {
		t.Fatal("failed startup retained capacity")
	}
}

func TestIsolatedFailedStartKeepsStopHandleAndLeaseForRetry(t *testing.T) {
	var stops atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error {
			if stops.Add(1) == 1 {
				return fmt.Errorf("first stop unavailable")
			}
			return nil
		}}, fmt.Errorf("workspace_unavailable: metadata write failed")
	}})
	resp, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, resp.GatewayID, "error")
	state := m.GatewayStates()[0]
	if state.PID != 123 || len(m.ports.ListUsed()) != 1 {
		t.Fatal("lost live handle or released its reservation")
	}
	if err := m.DeleteGateway(context.Background(), resp.GatewayID); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 2 || len(m.ports.ListUsed()) != 0 {
		t.Fatal("delete did not retry failed stop")
	}
}
