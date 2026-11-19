package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type networkPreparationStarter struct {
	isolatedTestStarter
	prepare func(context.Context, CreateGatewayRequest, int) (func(context.Context) error, error)
}

func (s networkPreparationStarter) PrepareGateway(ctx context.Context, req CreateGatewayRequest, port int) (func(context.Context) error, error) {
	return s.prepare(ctx, req, port)
}

func TestNetworkPreparationPrecedesNativeHooksAndCleanup(t *testing.T) {
	var prepared, stopped, cleaned atomic.Bool
	profile := isolatedTestProfile{prepare: func(CreateGatewayRequest) error {
		if !prepared.Load() {
			t.Error("native preparation preceded network")
		}
		return nil
	}}
	starter := networkPreparationStarter{prepare: func(context.Context, CreateGatewayRequest, int) (func(context.Context) error, error) {
		prepared.Store(true)
		return func(context.Context) error {
			if !stopped.Load() {
				t.Error("network cleaned before writer proof")
			}
			cleaned.Store(true)
			return nil
		}, nil
	}, isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{Stop: func(context.Context) error { stopped.Store(true); return nil }}, nil
	}}}
	cfg := Config{RuntimeType: "hermes", Runtime: profile, WorkspaceRoot: t.TempDir(), IsolatedLifecycle: true, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	m := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
	req := isolatedRequest(m, 1, 1, 20000)
	res, err := m.CreateGateway(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, res.GatewayID, "running")
	if err = m.StopGatewayConfirmed(context.Background(), res.GatewayID); err != nil {
		t.Fatal(err)
	}
	if !cleaned.Load() {
		t.Fatal("network cleanup did not precede stop receipt")
	}
}
func TestNetworkPreparationFailureNeverRunsNativeHooks(t *testing.T) {
	var native atomic.Bool
	starter := networkPreparationStarter{prepare: func(context.Context, CreateGatewayRequest, int) (func(context.Context) error, error) {
		return nil, errors.New("network unavailable")
	}, isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) { native.Store(true); return ManagedProcess{}, nil }}}
	cfg := Config{RuntimeType: "hermes", Runtime: isolatedTestProfile{prepare: func(CreateGatewayRequest) error { native.Store(true); return nil }}, WorkspaceRoot: t.TempDir(), IsolatedLifecycle: true, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	m := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
	res, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, res.GatewayID, "error")
	if native.Load() {
		t.Fatal("network setup failure executed native hook/process")
	}
}

func TestNetworkCleanupFailureNeverReleasesStopReceipt(t *testing.T) {
	starter := networkPreparationStarter{prepare: func(context.Context, CreateGatewayRequest, int) (func(context.Context) error, error) {
		return func(context.Context) error { return errors.New("namespace cleanup uncertain") }, nil
	}, isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{Stop: func(context.Context) error { return nil }}, nil
	}}}
	cfg := Config{RuntimeType: "hermes", Runtime: isolatedTestProfile{}, WorkspaceRoot: t.TempDir(), IsolatedLifecycle: true, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	m := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
	res, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, res.GatewayID, "running")
	if err = m.StopGatewayConfirmed(context.Background(), res.GatewayID); err == nil {
		t.Fatal("uncertain network cleanup produced confirmed stop receipt")
	}
	if err = m.StopGatewayConfirmed(context.Background(), res.GatewayID); err == nil {
		t.Fatal("repeated stop forgot uncertain network cleanup")
	}
}
