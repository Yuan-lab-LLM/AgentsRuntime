package openclaw

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type blockingMaintenanceHost struct {
	gateway.UpgradeHost
	calls            atomic.Int32
	entered, release chan struct{}
}

func (h *blockingMaintenanceHost) VerifyUpgradeWriter(context.Context, WorkspaceUpgradeRequest) error {
	if h.calls.Add(1) == 1 {
		return nil
	}
	close(h.entered)
	<-h.release
	return nil
}

func TestProtectedMaintenanceDeadlineCancelsButJoinsBlockedLeaseRead(t *testing.T) {
	req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 1, UID: 200007, GID: 200007, RolloutID: "12"}
	host := &blockingMaintenanceHost{entered: make(chan struct{}), release: make(chan struct{})}
	m := &upgradeService{cfg: gateway.Config{IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", PodUID: "maintenance-pod"}, host: host, io: &upgradeIO{operation: req}}
	ctx, finish, err := m.protectedMaintenanceContext(context.Background(), req, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(host.release); finish() }()
	select {
	case <-host.entered:
	case <-time.After(6 * time.Second):
		t.Fatal("maintenance lease check did not run")
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("blocked filesystem read suppressed operation deadline")
	}
	finished := make(chan struct{})
	go func() { finish(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("maintenance returned before blocked ownership check joined")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProtectedMaintenanceRequiresExactOperationAndWriterLease(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", PodUID: "maintenance-pod"}
	host := gateway.NewGatewayManager(cfg, nil, nil)
	req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 1, UID: 200007, GID: 200007, RolloutID: "12", LeaseToken: "operation-token"}
	m := &upgradeService{cfg: cfg, host: host, io: &upgradeIO{operation: req}}
	ctx, finish, err := m.protectedMaintenanceContext(context.Background(), req, true)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if ctx.Err() != nil {
		t.Fatal("readonly scope unexpectedly canceled caller")
	}
	changed := req
	changed.Generation++
	if _, _, err := m.protectedMaintenanceContext(context.Background(), changed, true); err == nil {
		t.Fatal("foreign operation authorized")
	}
	if _, _, err := m.protectedMaintenanceContext(context.Background(), req, false); err == nil {
		t.Fatal("write operation authorized without writer lease")
	}
	if _, err := host.AcquireWriterLease(req, req.LeaseToken, time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err = m.protectedMaintenanceContext(context.Background(), req, false)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if ctx.Err() == nil {
		t.Fatal("maintenance cleanup did not cancel its watcher")
	}
	m.io.commandUncertain = true
	if _, _, err := m.protectedMaintenanceContext(context.Background(), req, true); err == nil {
		t.Fatal("uncertain command allowed another maintenance execution")
	}
}

func TestProtectedMaintenanceWriterRevocationCancelsCommandContext(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", PodUID: "maintenance-pod"}
	host := gateway.NewGatewayManager(cfg, nil, nil)
	req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 1, UID: 200007, GID: 200007, RolloutID: "12", LeaseToken: "operation-token"}
	if _, err := host.AcquireWriterLease(req, req.LeaseToken, time.Minute); err != nil {
		t.Fatal(err)
	}
	m := &upgradeService{cfg: cfg, host: host, io: &upgradeIO{operation: req}}
	ctx, finish, err := m.protectedMaintenanceContext(context.Background(), req, false)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err := host.ReleaseWriterLease(req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(7 * time.Second):
		t.Fatal("revoked maintenance writer not canceled by five-second check")
	}
}

func TestProtectedReadonlyMaintenanceRetainsUncertainCapsule(t *testing.T) {
	m, req, workspace := scopedUpgradeService(t)
	m.cfg.NetworkSecurityProfile = "isolated-network-v1"
	m.cfg.IsolatedLifecycle = true
	m.cfg.PodUID = "maintenance-pod"
	m.cfg.GatewayBaseEnv = []string{"PATH=/usr/bin:/bin"}
	probe := filepath.Join(m.io.controlRoot(), "clawmanager-upgrade-retained")
	if err := m.io.MkdirAll(probe, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	cmd.Dir = probe
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	spec, err := m.upgradeSandboxSpec(cmd, req, true)
	if err != nil || !spec.OfflineNetwork || !spec.Mounts[0].ReadOnly || spec.Mounts[0].Source != workspace {
		t.Fatalf("unsafe protected readonly scope: %+v %v", spec, err)
	}
	m.io.commandUncertain = true
	m.removeUpgradeProbe(probe)
	if _, err := os.Stat(probe); err != nil {
		t.Fatal("uncertain readonly evidence removed", err)
	}
	if err := m.io.RemoveAll(probe); err == nil {
		t.Fatal("uncertain readonly capsule writable")
	}
}
