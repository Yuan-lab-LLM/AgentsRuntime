package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"syscall"
)

type upgradeService struct {
	scopedNative    bool
	scopedUncertain bool
	io              *upgradeIO
	cfg             gateway.Config
	host            gateway.UpgradeHost
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

// RunScopedNativeUpgrade is reachable only through the fixed credential helper.
// Its parent owns the operation lock, writer lease and container stop boundary.
func (p Profile) RunScopedNativeUpgrade(ctx context.Context, cfg gateway.Config, op gateway.NativeUpgradeOperation, host gateway.UpgradeHost) (json.RawMessage, error) {
	if cfg.SandboxProfile != "nfs-rootless-v1" || op.Request.LeaseToken != "" {
		return nil, fmt.Errorf("invalid scoped native upgrade")
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	if err := workspaceio.ValidateInstanceCaller(op.Request.UID, op.Request.GID); err != nil {
		return nil, err
	}
	service := &upgradeService{cfg: cfg, host: host, scopedNative: true}
	var result any
	var err error
	switch op.Action {
	case "compatibility":
		result, err = service.PreflightUpgradeCompatibility(ctx, op.Request)
	case "inventory":
		result, err = service.PreflightWorkspace(ctx, op.Request)
	case "migrate":
		result, err = service.MigrateSessionSQLite(ctx, op.Request)
	case "migration-status":
		result, err = service.SessionSQLiteMigrationStatus(op.Request)
	case "restore":
		result, err = service.RestoreSessionSQLite(ctx, op.Request)
	case "restore-status":
		result, err = service.SessionSQLiteRestoreStatus(op.Request)
	default:
		return nil, fmt.Errorf("unsupported native upgrade action")
	}
	if service.scopedUncertain || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ENOTCONN) {
		err = errors.Join(gateway.ErrNativeStorageUncertain, err)
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return raw, err
}
