package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type upgradeService struct {
	io   *upgradeIO
	cfg  gateway.Config
	host gateway.UpgradeHost
}

func (p Profile) UpgradeService(cfg gateway.Config, host gateway.UpgradeHost) gateway.RuntimeUpgradeService {
	return &upgradeService{cfg: cfg, host: host}
}

type WorkspaceUpgradeRequest = gateway.WorkspaceUpgradeRequest
type SessionSQLiteMigrationResult = gateway.SessionSQLiteMigrationResult
type SessionSQLiteRestoreResult = gateway.SessionSQLiteRestoreResult
type UpgradeCompatibilityResult = gateway.UpgradeCompatibilityResult
type WorkspaceInventory = gateway.WorkspaceInventory
type DatabasePreflight = gateway.DatabasePreflight
type SnapshotManifest = gateway.SnapshotManifest
type SnapshotFileFact = gateway.SnapshotFileFact

var pathWithin = gateway.PathWithin
var configureGatewayCommand = gateway.ConfigureGatewayCommand
var inheritUpgradeOperationFence = gateway.InheritUpgradeOperationFence
var fenceUpgradeCommand = gateway.FenceUpgradeCommand
var availableFilesystemBytes = gateway.AvailableFilesystemBytes
var validateUpgradeID = gateway.ValidateUpgradeID
var ErrWorkspacePath = gateway.ErrWorkspacePath
var ErrRuntimeType = gateway.ErrRuntimeType
