package gateway

import (
	"context"
	"io/fs"
	"os/exec"
)

type RuntimeUpgradeService interface {
	PreflightUpgradeCompatibility(context.Context, WorkspaceUpgradeRequest) (UpgradeCompatibilityResult, error)
	PreflightWorkspace(context.Context, WorkspaceUpgradeRequest) (WorkspaceInventory, error)
	MigrateSessionSQLite(context.Context, WorkspaceUpgradeRequest) (SessionSQLiteMigrationResult, error)
	SessionSQLiteMigrationStatus(WorkspaceUpgradeRequest) (SessionSQLiteMigrationResult, error)
	RestoreSessionSQLite(context.Context, WorkspaceUpgradeRequest) (SessionSQLiteRestoreResult, error)
	SessionSQLiteRestoreStatus(WorkspaceUpgradeRequest) (SessionSQLiteRestoreResult, error)
}
type RuntimeUpgradeProvider interface {
	UpgradeService(Config, UpgradeHost) RuntimeUpgradeService
}

// UpgradeHost exposes only shared validation, ownership and operation fencing.
type UpgradeHost interface {
	WorkspaceForUpgrade(WorkspaceUpgradeRequest) (string, error)
	UpgradeRoot() string
	InstanceActive(int) bool
	LockUpgradeOperation(int) (func(), error)
	UpgradeOperationContext(context.Context, int) context.Context
	KeepUpgradeWriterLease(context.Context, WorkspaceUpgradeRequest) (context.Context, func() error, error)
	VerifyUpgradeWriter(context.Context, WorkspaceUpgradeRequest) error
}

func (m *GatewayManager) nativeUpgrade() (RuntimeUpgradeService, error) {
	provider, ok := m.profile().(RuntimeUpgradeProvider)
	if !ok {
		return nil, ErrRuntimeType
	}
	service := provider.UpgradeService(m.cfg, m)
	if service == nil {
		return nil, ErrRuntimeType
	}
	return service, nil
}
func (m *GatewayManager) PreflightUpgradeCompatibility(ctx context.Context, req WorkspaceUpgradeRequest) (UpgradeCompatibilityResult, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return UpgradeCompatibilityResult{}, err
	}
	return service.PreflightUpgradeCompatibility(ctx, req)
}
func (m *GatewayManager) PreflightWorkspace(ctx context.Context, req WorkspaceUpgradeRequest) (WorkspaceInventory, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return WorkspaceInventory{}, err
	}
	return service.PreflightWorkspace(ctx, req)
}
func (m *GatewayManager) MigrateSessionSQLite(ctx context.Context, req WorkspaceUpgradeRequest) (SessionSQLiteMigrationResult, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return SessionSQLiteMigrationResult{}, err
	}
	return service.MigrateSessionSQLite(ctx, req)
}
func (m *GatewayManager) SessionSQLiteMigrationStatus(req WorkspaceUpgradeRequest) (SessionSQLiteMigrationResult, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return SessionSQLiteMigrationResult{}, err
	}
	return service.SessionSQLiteMigrationStatus(req)
}
func (m *GatewayManager) RestoreSessionSQLite(ctx context.Context, req WorkspaceUpgradeRequest) (SessionSQLiteRestoreResult, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return SessionSQLiteRestoreResult{}, err
	}
	return service.RestoreSessionSQLite(ctx, req)
}
func (m *GatewayManager) SessionSQLiteRestoreStatus(req WorkspaceUpgradeRequest) (SessionSQLiteRestoreResult, error) {
	service, err := m.nativeUpgrade()
	if err != nil {
		return SessionSQLiteRestoreResult{}, err
	}
	return service.SessionSQLiteRestoreStatus(req)
}
func (m *GatewayManager) WorkspaceForUpgrade(req WorkspaceUpgradeRequest) (string, error) {
	return m.workspaceForUpgrade(req)
}
func (m *GatewayManager) UpgradeRoot() string { return m.upgradeRoot() }
func (m *GatewayManager) LockUpgradeOperation(id int) (func(), error) {
	return m.lockUpgradeOperation(id)
}
func (m *GatewayManager) UpgradeOperationContext(ctx context.Context, id int) context.Context {
	return m.upgradeOperationContext(ctx, id)
}
func (m *GatewayManager) KeepUpgradeWriterLease(ctx context.Context, req WorkspaceUpgradeRequest) (context.Context, func() error, error) {
	return m.keepUpgradeWriterLease(ctx, req)
}
func (m *GatewayManager) VerifyUpgradeWriter(ctx context.Context, req WorkspaceUpgradeRequest) error {
	return m.verifyUpgradeWriter(ctx, req)
}
func ConfigureGatewayCommand(cmd *exec.Cmd, uid, gid int) { configureGatewayCommand(cmd, uid, gid) }
func InheritUpgradeOperationFence(ctx context.Context, cmd *exec.Cmd) {
	inheritUpgradeOperationFence(ctx, cmd)
}
func FenceUpgradeCommand(cmd *exec.Cmd) func()             { return fenceUpgradeCommand(cmd) }
func AvailableFilesystemBytes(path string) (uint64, error) { return availableFilesystemBytes(path) }
func CopyRegularFile(source, target string, mode fs.FileMode) error {
	return copyRegularFile(source, target, mode)
}
func AtomicUpgradeWriteFile(path string, data []byte, mode fs.FileMode) error {
	return atomicWriteFile(path, data, mode)
}
func ValidateUpgradeID(label, value string) error { return validateUpgradeID(label, value) }
