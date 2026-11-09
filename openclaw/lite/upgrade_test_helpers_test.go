package openclaw

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"time"
)

type GatewayStartSpec = gateway.GatewayStartSpec
type ManagedProcess = gateway.ManagedProcess
type upgradeTestStarter struct{}

func (*upgradeTestStarter) StartGateway(context.Context, GatewayStartSpec) (ManagedProcess, error) {
	return ManagedProcess{}, errors.New("not used")
}

type nativeTestManager struct {
	*upgradeService
	manager *gateway.GatewayManager
}

func NewGatewayManager(cfg gateway.Config, starter gateway.ProcessStarter, ports *gateway.PortAllocator) *nativeTestManager {
	cfg.Runtime = NewProfile("openclaw")
	manager := gateway.NewGatewayManager(cfg, starter, ports)
	return &nativeTestManager{upgradeService: &upgradeService{cfg: cfg, host: manager, io: newFixtureUpgradeIO(cfg.WorkspaceRoot)}, manager: manager}
}
func (m *nativeTestManager) workspaceForUpgrade(req WorkspaceUpgradeRequest) (string, error) {
	return m.host.WorkspaceForUpgrade(req)
}
func (m *nativeTestManager) AcquireWriterLease(req WorkspaceUpgradeRequest, token string, ttl time.Duration) (gateway.WriterLease, error) {
	return m.manager.AcquireWriterLease(req, token, ttl)
}
func (m *nativeTestManager) ReleaseWriterLease(req WorkspaceUpgradeRequest) error {
	return m.manager.ReleaseWriterLease(req)
}
func (m *nativeTestManager) CreateWorkspaceSnapshot(ctx context.Context, req WorkspaceUpgradeRequest) (SnapshotManifest, error) {
	return m.manager.CreateWorkspaceSnapshot(ctx, req)
}
func (m *nativeTestManager) VerifyWorkspaceSnapshot(req WorkspaceUpgradeRequest) (SnapshotManifest, error) {
	return m.manager.VerifyWorkspaceSnapshot(req)
}
func (m *nativeTestManager) RestoreWorkspaceSnapshot(ctx context.Context, req WorkspaceUpgradeRequest) (gateway.RestoreResult, error) {
	return m.manager.RestoreWorkspaceSnapshot(ctx, req)
}
