package openclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

func (m *upgradeService) resumeValidatedMigration(ctx context.Context, workspace string, req WorkspaceUpgradeRequest, result SessionSQLiteMigrationResult) (SessionSQLiteMigrationResult, bool, error) {
	if err := validateUpgradeID("resume_rollout_id", req.ResumeRolloutID); err != nil {
		return result, false, err
	}
	priorReq := req
	priorReq.RolloutID = req.ResumeRolloutID
	priorReq.Generation = req.ResumeGeneration
	prior, err := m.SessionSQLiteMigrationStatus(priorReq)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if prior.Status != "validated" {
		return result, false, nil
	}
	if _, err := m.io.Stat(m.sessionRestoreReceiptPath(priorReq)); !errors.Is(err, os.ErrNotExist) {
		return result, false, errors.New("previous migration was restored or its restoration state is ambiguous")
	}
	raw, err := m.io.ReadFile(filepath.Join(workspace, "home", ".openclaw", "openclaw.json"))
	if err != nil {
		return result, false, err
	}
	hash := sha256.Sum256(raw)
	// Doctor adds its own metadata after the prepared-config hash was recorded.
	// A byte mismatch is not evidence of corrupt data. Validate current config
	// read-only with the target core; never normalize or overwrite it on resume.
	if err := m.runOpenClawUpgradeCommand(ctx, workspace, filepath.Join(workspace, "home"), req, "recovery-config-validate", []string{"config", "validate", "--json"}, io.Discard); err != nil {
		return result, false, err
	}
	count, catalog, err := m.inspectOfficialSessionCatalog(ctx, workspace, filepath.Join(workspace, "home"), req)
	if err != nil {
		return result, false, err
	}
	if count < prior.SessionCount {
		return result, false, errors.New("session history no longer matches the validated migration")
	}
	if count == prior.SessionCount && catalog != prior.SessionCatalogSHA256 {
		return result, false, errors.New("session identities no longer match the validated migration")
	}
	result.Status = "validated"
	result.ConfigOriginalSHA256 = hex.EncodeToString(hash[:])
	result.ConfigTargetSHA256 = result.ConfigOriginalSHA256
	result.OutputSHA256 = prior.OutputSHA256
	result.SessionCount = count
	result.SessionCatalogSHA256 = catalog
	result.CompletedAt = time.Now().UTC()
	if result.PhaseDurationsMS == nil {
		result.PhaseDurationsMS = map[string]int64{}
	}
	result.PhaseDurationsMS["reused_validated_migration"] = 0
	return result, true, m.writeSessionMigrationReceipt(m.sessionMigrationReceiptPath(req), result)
}
