package slots

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"sync/atomic"
	"testing"
)

func TestNetworkOwnerIdempotencyConflictsAndAuxiliaryFence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var builds, closed atomic.Int32
	owner := &networkOwner{build: func(context.Context, gateway.Config, gateway.CreateGatewayRequest, int) (*networkSession, error) {
		builds.Add(1)
		return newTestNetworkSession(ctx, func(context.Context) error { closed.Add(1); return nil }), nil
	}}
	req := gateway.CreateGatewayRequest{InstanceID: 1, UserID: 2, Generation: 3, UID: 200001, GID: 200001, WorkspacePath: "/workspaces/hermes/user-2/instance-1", AgentType: "hermes"}
	cfg := gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}
	cleanup, err := owner.prepare(ctx, cfg, req, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.prepare(ctx, cfg, req, 20000); err != nil || builds.Load() != 1 {
		t.Fatalf("same request replaced session: %v %d", err, builds.Load())
	}
	conflicting := req
	conflicting.Generation++
	if _, err = owner.prepare(ctx, cfg, conflicting, 20000); err == nil {
		t.Fatal("conflicting generation replaced active session")
	}
	spec := gateway.GatewayStartSpec{InstanceID: 1, UserID: 2, Generation: 3, UID: 200001, GID: 200001, WorkspacePath: req.WorkspacePath, RuntimeType: "hermes", Port: 20000}
	_, finish, err := owner.join(spec)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cleanup(context.Background()) }()
	select {
	case <-done:
		t.Fatal("cleanup escaped unjoined auxiliary")
	default:
	}
	finish(nil)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatal("resources not closed")
	}
	if _, _, err = owner.join(spec); err == nil {
		t.Fatal("closed session admitted auxiliary")
	}
}
func TestNetworkOwnerSetupFailureAndUncertainWriterNeverFallback(t *testing.T) {
	owner := &networkOwner{build: func(context.Context, gateway.Config, gateway.CreateGatewayRequest, int) (*networkSession, error) {
		return nil, errors.New("denied")
	}}
	if _, err := owner.prepare(context.Background(), gateway.Config{}, gateway.CreateGatewayRequest{}, 1); err == nil {
		t.Fatal("failed setup admitted")
	}
	if _, _, err := owner.join(gateway.GatewayStartSpec{}); err == nil {
		t.Fatal("missing session joined outer network")
	}
}
func newTestNetworkSession(ctx context.Context, close func(context.Context) error) *networkSession {
	ctx, cancel := context.WithCancel(ctx)
	return &networkSession{ctx: ctx, cancel: cancel, release: close}
}

func TestNetworkSessionUncertainAuxiliaryRetainsResources(t *testing.T) {
	var releases atomic.Int32
	owner := &networkOwner{build: func(ctx context.Context, _ gateway.Config, _ gateway.CreateGatewayRequest, _ int) (*networkSession, error) {
		return newTestNetworkSession(ctx, func(context.Context) error { releases.Add(1); return nil }), nil
	}}
	req := gateway.CreateGatewayRequest{InstanceID: 1}
	cleanup, err := owner.prepare(context.Background(), gateway.Config{}, req, 20000)
	if err != nil {
		t.Fatal(err)
	}
	_, finish, err := owner.join(gateway.GatewayStartSpec{InstanceID: 1, Port: 20000})
	if err != nil {
		t.Fatal(err)
	}
	finish(gateway.ErrGatewayStopFailed)
	if err = cleanup(context.Background()); !errors.Is(err, gateway.ErrGatewayStopFailed) {
		t.Fatalf("uncertain writer released session: %v", err)
	}
	if releases.Load() != 0 || owner.active == nil {
		t.Fatal("uncertain namespace resources/evidence discarded")
	}
}
