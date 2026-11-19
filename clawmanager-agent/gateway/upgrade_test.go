package gateway

import (
	"context"

	"errors"
	"os"

	"testing"
	"time"
)

func TestStopGatewayConfirmedPreservesBindingOnStopError(t *testing.T) {
	root := t.TempDir()
	cfg := Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayPortStart: 31000, GatewayPortEnd: 31000, GatewayPortBlockSize: 1, Capacity: 1}
	ports := NewPortAllocator(func(int) bool { return false })
	if _, err := ports.ReserveExact(7, 1, 31000); err != nil {
		t.Fatal(err)
	}
	mgr := NewGatewayManager(cfg, &upgradeTestStarter{}, ports)
	mgr.gateways["gw-7-1"] = &gatewayRecord{
		state:   GatewayState{GatewayID: "gw-7-1", InstanceID: 7, Generation: 1, Port: 31000, State: "running"},
		process: ManagedProcess{PID: 99, Stop: func(context.Context) error { return errors.New("still alive") }},
	}
	if err := mgr.StopGatewayConfirmed(context.Background(), "gw-7-1"); !errors.Is(err, ErrGatewayStopFailed) {
		t.Fatalf("stop error = %v", err)
	}
	state, ok := mgr.GatewayState("gw-7-1")
	if !ok || state.State != "stop_error" {
		t.Fatalf("state = %+v ok=%v", state, ok)
	}
	if used := ports.ListUsed(); len(used) != 1 || used[0] != 31000 {
		t.Fatalf("reserved ports = %#v", used)
	}
}

func TestStopGatewayConfirmedOwnsConcurrentProcessExit(t *testing.T) {
	root := t.TempDir()
	cfg := Config{RuntimeType: "openclaw", WorkspaceRoot: root, GatewayPortStart: 31000, GatewayPortEnd: 31000, GatewayPortBlockSize: 1, Capacity: 1}
	ports := NewPortAllocator(func(int) bool { return false })
	if _, err := ports.ReserveExact(7, 1, 31000); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	stopReturned := make(chan struct{})
	mgr := NewGatewayManager(cfg, &upgradeTestStarter{}, ports)
	mgr.gateways["gw-7-1"] = &gatewayRecord{
		state: GatewayState{GatewayID: "gw-7-1", InstanceID: 7, Generation: 1, Port: 31000, PID: 99, State: "running"},
		process: ManagedProcess{PID: 99, Done: done, Stop: func(context.Context) error {
			done <- nil
			<-stopReturned
			return nil
		}},
	}
	go mgr.watchGatewayProcess("gw-7-1", 99, done)

	result := make(chan error, 1)
	go func() { result <- mgr.StopGatewayConfirmed(context.Background(), "gw-7-1") }()
	deadline := time.Now().Add(time.Second)
	for {
		state, ok := mgr.GatewayState("gw-7-1")
		if ok && state.State == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher did not observe stopped state: %+v ok=%v", state, ok)
		}
		time.Sleep(time.Millisecond)
	}
	close(stopReturned)
	if err := <-result; err != nil {
		t.Fatalf("confirmed stop failed after watcher observed exit: %v", err)
	}
	if _, ok := mgr.GatewayState("gw-7-1"); ok {
		t.Fatal("gateway binding was not removed after confirmed stop")
	}
	if used := ports.ListUsed(); len(used) != 0 {
		t.Fatalf("reserved ports = %#v", used)
	}
}

func TestGatewayWatcherIgnoresReplacedProcess(t *testing.T) {
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", WorkspaceRoot: t.TempDir()}, &upgradeTestStarter{}, nil)
	done := make(chan error, 1)
	mgr.gateways["gw-7-1"] = &gatewayRecord{
		state:   GatewayState{GatewayID: "gw-7-1", InstanceID: 7, Generation: 2, PID: 100, State: "running"},
		process: ManagedProcess{PID: 100},
	}
	done <- nil
	mgr.watchGatewayProcess("gw-7-1", 99, done)
	state, ok := mgr.GatewayState("gw-7-1")
	if !ok || state.State != "running" || state.PID != 100 {
		t.Fatalf("replacement process was changed by stale watcher: %+v ok=%v", state, ok)
	}
}

func TestWriterLeaseBlocksGatewayUntilReleased(t *testing.T) {
	root := t.TempDir()
	cfg := Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target", GatewayPortStart: 32000, GatewayPortEnd: 32000, GatewayPortBlockSize: 1, Capacity: 1}
	mgr := NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-lease", InstanceID: 19, UserID: 7, Generation: 3, LeaseToken: "lease-token-2"}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	create := CreateGatewayRequest{AgentType: "openclaw", InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, WorkspacePath: workspace, GatewayPort: 32000}
	if _, err := mgr.CreateGateway(context.Background(), create); err == nil {
		t.Fatal("gateway creation succeeded while writer lease was active")
	}
	if err := mgr.ReleaseWriterLease(req); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateGateway(context.Background(), create); err != nil {
		t.Fatalf("gateway creation after lease release: %v", err)
	}
	// The stub rejects startup asynchronously. Wait before TempDir cleanup so
	// workspace preparation cannot recreate directories during RemoveAll.
	waitForGatewayState(t, mgr, gatewayID(req.InstanceID, req.Generation), "error")
}

func TestUpgradeStandbyRequiresMatchingDurableActivation(t *testing.T) {
	root := t.TempDir()
	cfg := Config{RuntimeType: "openclaw", OpenClawVersion: "2026.8.1", UpgradeID: "81", WorkspaceRoot: root, PodUID: "pod-target", GatewayPortStart: 32000, GatewayPortEnd: 32000, GatewayPortBlockSize: 1, Capacity: 1}
	mgr := NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	if !mgr.UpgradeStandby() || mgr.ReadyForTraffic() {
		t.Fatal("upgrade target must start in standby")
	}
	if payload := mgr.RegisterPayload(); payload.State != "standby" || payload.AvailableSlots != 0 {
		t.Fatalf("standby payload = %+v", payload)
	}
	if err := mgr.ActivateUpgrade("different"); err == nil {
		t.Fatal("mismatched rollout activated target")
	}
	if err := mgr.ActivateUpgrade("81"); err != nil {
		t.Fatal(err)
	}
	if mgr.UpgradeStandby() || !mgr.ReadyForTraffic() {
		t.Fatal("matching activation did not release target")
	}
	if payload := mgr.HeartbeatPayload(1); payload.State != "ready" || payload.AvailableSlots != 1 {
		t.Fatalf("activated heartbeat = %+v", payload)
	}
	restarted := NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	if restarted.UpgradeStandby() || !restarted.ReadyForTraffic() {
		t.Fatal("activation marker was not durable across restart")
	}
}

type upgradeTestStarter struct{}

func (*upgradeTestStarter) StartGateway(context.Context, GatewayStartSpec) (ManagedProcess, error) {
	return ManagedProcess{}, errors.New("not used")
}
