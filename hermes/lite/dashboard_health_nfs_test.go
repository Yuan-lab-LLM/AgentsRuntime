package hermes

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestNFSHealthRequiresScopedWorkspaceReadAndPropagatesFailure(t *testing.T) {
	checker, spec := newDashboardHealthFixture(t, &dashboardFixture{})
	checker.cfg.SandboxProfile = "nfs-rootless-v1"
	if _, err := newDashboardProbe(context.Background(), checker.cfg, spec); err == nil {
		t.Fatal("NFS health accepted management-identity private workspace inspection")
	}
	want := errors.New("private workspace read denied")
	checker.cfg.WorkspaceHealthCheck = func(_ context.Context, got gateway.GatewayStartSpec) error {
		if got.InstanceID != spec.InstanceID || got.WorkspacePath != spec.WorkspacePath {
			t.Fatal("health identity changed")
		}
		return want
	}
	if _, err := newDashboardProbe(context.Background(), checker.cfg, spec); !errors.Is(err, want) {
		t.Fatalf("scoped private read failure not propagated: %v", err)
	}
	checker.cfg.WorkspaceHealthCheck = func(context.Context, gateway.GatewayStartSpec) error { return nil }
	probe, err := newDashboardProbe(context.Background(), checker.cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	probe.client.CloseIdleConnections()
}
