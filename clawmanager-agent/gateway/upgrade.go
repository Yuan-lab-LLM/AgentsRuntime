package gateway

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"path/filepath"
	"regexp"

	"strconv"
	"strings"
	"time"
)

var safeUpgradeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type WorkspaceUpgradeRequest struct {
	RolloutID               string `json:"rollout_id"`
	SnapshotID              string `json:"snapshot_id,omitempty"`
	InstanceID              int    `json:"instance_id"`
	UserID                  int    `json:"user_id"`
	Generation              int    `json:"generation"`
	LeaseToken              string `json:"lease_token,omitempty"`
	OfficialDBCheck         bool   `json:"official_database_check,omitempty"`
	PreserveSessionSQLite   bool   `json:"preserve_session_sqlite,omitempty"`
	UID                     int    `json:"uid,omitempty"`
	GID                     int    `json:"gid,omitempty"`
	RecoveryMode            string `json:"recovery_mode,omitempty"`
	ResumeRolloutID         string `json:"resume_rollout_id,omitempty"`
	ResumeGeneration        int    `json:"resume_generation,omitempty"`
	ManagedPluginPlanSHA256 string `json:"managed_plugin_plan_sha256,omitempty"`
}

type SessionSQLiteMigrationResult struct {
	InstanceID           int              `json:"instance_id"`
	RolloutID            string           `json:"rollout_id,omitempty"`
	Generation           int              `json:"generation,omitempty"`
	Status               string           `json:"status"`
	OutputSHA256         string           `json:"output_sha256"`
	ArchiveBytes         int64            `json:"archive_bytes"`
	ArchiveFiles         int64            `json:"archive_files"`
	RollbackAvailable    bool             `json:"rollback_available"`
	ConfigOriginalSHA256 string           `json:"config_original_sha256,omitempty"`
	ConfigTargetSHA256   string           `json:"config_target_sha256,omitempty"`
	StateCapsuleBytes    int64            `json:"state_capsule_bytes,omitempty"`
	SessionCount         int              `json:"session_count,omitempty"`
	SessionCatalogSHA256 string           `json:"session_catalog_sha256,omitempty"`
	PhaseDurationsMS     map[string]int64 `json:"phase_durations_ms,omitempty"`
	CompletedAt          time.Time        `json:"completed_at"`
	DataPreserved        bool             `json:"data_preserved,omitempty"`
	PreservationSHA256   string           `json:"preservation_sha256,omitempty"`
}

type SessionSQLiteRestoreResult struct {
	InstanceID             int              `json:"instance_id"`
	RolloutID              string           `json:"rollout_id,omitempty"`
	Generation             int              `json:"generation,omitempty"`
	Status                 string           `json:"status"`
	OutputSHA256           string           `json:"output_sha256"`
	ConfigRestored         bool             `json:"config_restored"`
	StateRestored          bool             `json:"state_restored"`
	PreservedSessionSQLite bool             `json:"preserved_session_sqlite"`
	PhaseDurationsMS       map[string]int64 `json:"phase_durations_ms,omitempty"`
	CompletedAt            time.Time        `json:"completed_at"`
}

type UpgradeCompatibilityResult struct {
	ManagedPluginPlanSHA256 string    `json:"managed_plugin_plan_sha256,omitempty"`
	InstanceID              int       `json:"instance_id"`
	Status                  string    `json:"status"`
	ConfigOriginalSHA256    string    `json:"config_original_sha256"`
	ConfigTargetSHA256      string    `json:"config_target_sha256"`
	ConfigBytes             int64     `json:"config_bytes"`
	SessionBytes            int64     `json:"session_bytes"`
	StateBytes              int64     `json:"state_bytes"`
	AvailableBytes          uint64    `json:"available_bytes"`
	ConfigValidated         bool      `json:"config_validated"`
	DoctorValidated         bool      `json:"doctor_validated"`
	SessionDryRunValid      bool      `json:"session_dry_run_valid"`
	ProbeOutputSHA256       string    `json:"probe_output_sha256"`
	CheckedAt               time.Time `json:"checked_at"`
}

type WorkspaceInventory struct {
	WorkspacePath  string              `json:"workspace_path"`
	OpenClawHome   string              `json:"openclaw_home"`
	FileCount      int64               `json:"file_count"`
	DirectoryCount int64               `json:"directory_count"`
	SymlinkCount   int64               `json:"symlink_count"`
	TotalBytes     int64               `json:"total_bytes"`
	AvailableBytes uint64              `json:"available_bytes"`
	DatabaseFiles  []DatabasePreflight `json:"database_files"`
	CheckedAt      time.Time           `json:"checked_at"`
}

type DatabasePreflight struct {
	RelativePath string `json:"relative_path"`
	SizeBytes    int64  `json:"size_bytes"`
	SQLiteHeader bool   `json:"sqlite_header"`
	OfficialOK   bool   `json:"official_ok,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
}

type SnapshotManifest struct {
	SchemaVersion int                `json:"schema_version"`
	RolloutID     string             `json:"rollout_id"`
	SnapshotID    string             `json:"snapshot_id"`
	InstanceID    int                `json:"instance_id"`
	UserID        int                `json:"user_id"`
	Generation    int                `json:"generation"`
	WorkspacePath string             `json:"workspace_path"`
	ArchivePath   string             `json:"archive_path"`
	ArchiveSHA256 string             `json:"archive_sha256"`
	ArchiveBytes  int64              `json:"archive_bytes"`
	FileCount     int64              `json:"file_count"`
	TotalBytes    int64              `json:"total_bytes"`
	CreatedAt     time.Time          `json:"created_at"`
	Files         []SnapshotFileFact `json:"files"`
}

type SnapshotFileFact struct {
	Path    string      `json:"path"`
	Mode    fs.FileMode `json:"mode"`
	Size    int64       `json:"size"`
	ModTime time.Time   `json:"mod_time"`
	SHA256  string      `json:"sha256,omitempty"`
	Link    string      `json:"link,omitempty"`
}

type WriterLease struct {
	SchemaVersion int       `json:"schema_version"`
	RolloutID     string    `json:"rollout_id"`
	InstanceID    int       `json:"instance_id"`
	UserID        int       `json:"user_id"`
	Generation    int       `json:"generation"`
	Token         string    `json:"token"`
	Holder        string    `json:"holder"`
	ExpiresAt     time.Time `json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (m *GatewayManager) workspaceForUpgrade(req WorkspaceUpgradeRequest) (string, error) {
	if req.UserID <= 0 || req.InstanceID <= 0 || req.Generation <= 0 {
		return "", fmt.Errorf("%w: positive user, instance and generation are required", ErrWorkspacePath)
	}
	return ValidateWorkspacePath(m.cfg.WorkspaceRoot, m.cfg.RuntimeType, CreateGatewayRequest{
		UserID: req.UserID, InstanceID: req.InstanceID,
		WorkspacePath: filepath.Join(m.cfg.WorkspaceRoot, m.cfg.RuntimeType, "user-"+strconv.Itoa(req.UserID), "instance-"+strconv.Itoa(req.InstanceID)),
	})
}

func validateUpgradeID(label, value string) error {
	if !safeUpgradeID.MatchString(strings.TrimSpace(value)) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func (m *GatewayManager) upgradeRoot() string {
	return filepath.Join(m.cfg.WorkspaceRoot, ".clawmanager-upgrades")
}

func (m *GatewayManager) leasePath(instanceID int) string {
	return filepath.Join(m.upgradeRoot(), "leases", "instance-"+strconv.Itoa(instanceID)+".json")
}

func (m *GatewayManager) AcquireWriterLease(req WorkspaceUpgradeRequest, token string, ttl time.Duration) (WriterLease, error) {
	if _, err := m.workspaceForUpgrade(req); err != nil {
		return WriterLease{}, err
	}
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return WriterLease{}, err
	}
	if err := validateUpgradeID("lease_token", token); err != nil {
		return WriterLease{}, err
	}
	if ttl < time.Minute || ttl > time.Hour {
		return WriterLease{}, errors.New("lease ttl must be between one minute and one hour")
	}
	unlock, lockErr := m.lockUpgradeOperation(req.InstanceID)
	if lockErr != nil {
		return WriterLease{}, lockErr
	}
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(m.leasePath(req.InstanceID)), 0o700); err != nil {
		return WriterLease{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instanceActiveLocked(req.InstanceID) {
		return WriterLease{}, fmt.Errorf("instance %d still has an active gateway", req.InstanceID)
	}
	now := time.Now().UTC()
	lease := WriterLease{SchemaVersion: 1, RolloutID: req.RolloutID, InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, Token: token, Holder: m.cfg.PodUID, ExpiresAt: now.Add(ttl), UpdatedAt: now}
	raw, _ := json.Marshal(lease)
	if existing, readErr := m.readWriterLease(req.InstanceID); readErr == nil {
		if existing.RolloutID == req.RolloutID && existing.InstanceID == req.InstanceID && existing.UserID == req.UserID && existing.Generation == req.Generation && existing.Token == token && existing.Holder == m.cfg.PodUID {
			if err := atomicWriteFile(m.leasePath(req.InstanceID), append(raw, '\n'), 0o600); err != nil {
				return WriterLease{}, err
			}
			return lease, nil
		}
		if existing.ExpiresAt.After(now) {
			return WriterLease{}, errors.New("writer lease is held by another upgrade operation")
		}
		if err := os.Remove(m.leasePath(req.InstanceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return WriterLease{}, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WriterLease{}, fmt.Errorf("read existing writer lease: %w", readErr)
	}
	file, err := os.OpenFile(m.leasePath(req.InstanceID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return WriterLease{}, fmt.Errorf("writer lease unavailable: %w", err)
	}
	if _, err = file.Write(append(raw, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return WriterLease{}, err
	}
	if closeErr != nil {
		return WriterLease{}, closeErr
	}
	return lease, nil
}

// writerLeaseBlocksGateway is called while the gateway manager mutex is held.
// A corrupt lease fails closed; an expired lease is removed so a crashed
// upgrade controller cannot permanently strand an instance.
func (m *GatewayManager) writerLeaseBlocksGateway(instanceID int) (bool, error) {
	lease, err := m.readWriterLease(instanceID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read workspace writer lease: %w", err)
	}
	if lease.ExpiresAt.After(time.Now().UTC()) {
		return true, nil
	}
	unlock, lockErr := m.lockUpgradeOperation(instanceID)
	if lockErr != nil {
		return true, lockErr
	}
	defer unlock()
	if err := os.Remove(m.leasePath(instanceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove expired workspace writer lease: %w", err)
	}
	return false, nil
}

func (m *GatewayManager) readWriterLease(instanceID int) (WriterLease, error) {
	var lease WriterLease
	raw, err := os.ReadFile(m.leasePath(instanceID))
	if err != nil {
		return lease, err
	}
	if err := json.Unmarshal(raw, &lease); err != nil {
		return lease, err
	}
	return lease, nil
}

func (m *GatewayManager) requireWriterLease(req WorkspaceUpgradeRequest) (WriterLease, error) {
	lease, err := m.readWriterLease(req.InstanceID)
	if err != nil {
		return lease, fmt.Errorf("read writer lease: %w", err)
	}
	if lease.RolloutID != req.RolloutID || lease.Token != req.LeaseToken || lease.Generation != req.Generation || lease.UserID != req.UserID || lease.Holder != m.cfg.PodUID {
		return lease, errors.New("writer lease identity mismatch")
	}
	if !lease.ExpiresAt.After(time.Now().UTC()) {
		return lease, errors.New("writer lease expired")
	}
	return lease, nil
}

func (m *GatewayManager) RenewWriterLease(req WorkspaceUpgradeRequest, ttl time.Duration) (WriterLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, err := m.requireWriterLease(req)
	if err != nil {
		return lease, err
	}
	if ttl < time.Minute || ttl > time.Hour {
		return lease, errors.New("lease ttl must be between one minute and one hour")
	}
	lease.UpdatedAt = time.Now().UTC()
	lease.ExpiresAt = lease.UpdatedAt.Add(ttl)
	raw, _ := json.Marshal(lease)
	if err := atomicWriteFile(m.leasePath(req.InstanceID), append(raw, '\n'), 0o600); err != nil {
		return WriterLease{}, err
	}
	return lease, nil
}

func (m *GatewayManager) ReleaseWriterLease(req WorkspaceUpgradeRequest) error {
	unlock, lockErr := m.lockUpgradeOperation(req.InstanceID)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.requireWriterLease(req); err != nil {
		return err
	}
	return os.Remove(m.leasePath(req.InstanceID))
}

func (m *GatewayManager) CreateWorkspaceSnapshot(ctx context.Context, req WorkspaceUpgradeRequest) (SnapshotManifest, error) {
	return SnapshotManifest{}, errors.New("full workspace snapshots are disabled; use the OpenClaw session migration capsule")
}

func (m *GatewayManager) VerifyWorkspaceSnapshot(req WorkspaceUpgradeRequest) (SnapshotManifest, error) {
	return SnapshotManifest{}, errors.New("full workspace snapshots are disabled; use the OpenClaw session migration capsule")
}

type RestoreResult struct {
	WorkspacePath  string `json:"workspace_path"`
	PreservedPath  string `json:"preserved_path,omitempty"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

func (m *GatewayManager) RestoreWorkspaceSnapshot(ctx context.Context, req WorkspaceUpgradeRequest) (RestoreResult, error) {
	return RestoreResult{}, errors.New("full workspace restore is disabled; use the OpenClaw session migration restore")
}

func copyRegularFile(source, target string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
