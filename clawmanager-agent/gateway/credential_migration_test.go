package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type migrationTestProfile struct {
	isolatedTestProfile
	migrate func() error
}

func (p migrationTestProfile) MigrateManagedCredentials(context.Context, Config, CreateGatewayRequest, string) error {
	return p.migrate()
}
func TestManagedCredentialMigrationLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "migration_error", "missing_adapter"} {
		t.Run(scenario, func(t *testing.T) {
			var network, migrated, native atomic.Bool
			base := isolatedTestProfile{prepare: func(CreateGatewayRequest) error {
				native.Store(true)
				if !migrated.Load() {
					t.Error("native preparation observed unmigrated credentials")
				}
				return nil
			}}
			var profile RuntimeProfile = migrationTestProfile{base, func() error {
				if !network.Load() {
					t.Error("migration preceded network preparation")
				}
				if scenario == "migration_error" {
					return errors.New("unsafe config")
				}
				migrated.Store(true)
				return nil
			}}
			if scenario == "missing_adapter" {
				profile = base
			}
			starter := networkPreparationStarter{prepare: func(context.Context, CreateGatewayRequest, int) (func(context.Context) error, error) {
				network.Store(true)
				return func(context.Context) error { return nil }, nil
			}, isolatedTestStarter: isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				return ManagedProcess{Stop: func(context.Context) error { return nil }}, nil
			}}}
			cfg := Config{RuntimeType: "hermes", Runtime: profile, WorkspaceRoot: t.TempDir(), IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
			m := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			res, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				awaitIsolatedState(t, m, res.GatewayID, "running")
				if !migrated.Load() {
					t.Fatal("migration omitted")
				}
			} else {
				awaitIsolatedState(t, m, res.GatewayID, "error")
				if native.Load() {
					t.Fatal("failed migration ran native code")
				}
			}
		})
	}
}
