package openclaw

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/openclawcompat"
)

type openClawSessionCatalog struct {
	Sessions []struct {
		Key       string `json:"key"`
		SessionID string `json:"sessionId"`
	} `json:"sessions"`
}

type upgradeConfigCapsule struct {
	SchemaVersion  int    `json:"schema_version"`
	InstanceID     int    `json:"instance_id"`
	OriginalExists bool   `json:"original_exists"`
	OriginalMode   uint32 `json:"original_mode"`
	OriginalSHA256 string `json:"original_sha256"`
	TargetSHA256   string `json:"target_sha256"`
}

type upgradeStateCapsule struct {
	SchemaVersion  int                                `json:"schema_version"`
	InstanceID     int                                `json:"instance_id"`
	OriginalExists bool                               `json:"original_exists"`
	TotalBytes     int64                              `json:"total_bytes"`
	FileCount      int64                              `json:"file_count"`
	WorkspaceFiles map[string]bool                    `json:"workspace_files"`
	ControlDirs    map[string]bool                    `json:"control_dirs"`
	ControlFiles   map[string]upgradeStateCapsuleFile `json:"control_files,omitempty"`
}

type upgradeStateCapsuleFile struct {
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

const maxUpgradeConfigBytes = 1 << 20

var errUpgradeProbeBusy = errors.New("upgrade probe source is temporarily busy")
var errUpgradeStateSchemaRequired = errors.New("OpenClaw state schema requires official Doctor migration")

// PreflightUpgradeCompatibility rehearses the complete 8.1 repair and session
// dry-run in an isolated HOME. OpenClaw CLI startup is allowed to create and
// migrate SQLite state, so pointing HOME at the live workspace is not a
// read-only preflight. The probe contains only state/session migration inputs,
// plugin metadata and the two workspace files doctor may migrate.
func (m *upgradeService) PreflightUpgradeCompatibility(ctx context.Context, req WorkspaceUpgradeRequest) (UpgradeCompatibilityResult, error) {
	ctx = withReadOnlyUpgradeCommands(ctx)
	closeIO, ioErr := m.beginIO(req, true)
	if ioErr != nil {
		return UpgradeCompatibilityResult{}, ioErr
	}
	defer closeIO()

	result := UpgradeCompatibilityResult{InstanceID: req.InstanceID}
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return result, err
	}
	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return result, err
	}
	if !IsOpenClawAtLeast(m.cfg.OpenClawVersion, OpenClaw81Version) {
		return result, fmt.Errorf("OpenClaw %s does not support upgrade compatibility preflight", m.cfg.OpenClawVersion)
	}
	if req.UID <= 0 || req.GID <= 0 {
		return result, errors.New("positive uid and gid are required for upgrade compatibility preflight")
	}
	inventory, err := m.PreflightWorkspace(ctx, req)
	if err != nil {
		return result, err
	}
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	raw, readErr := m.io.ReadFile(configPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return result, fmt.Errorf("read OpenClaw config: %w", readErr)
	}
	if len(raw) > maxUpgradeConfigBytes {
		return result, fmt.Errorf("OpenClaw config exceeds %d byte upgrade limit", maxUpgradeConfigBytes)
	}
	config := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return result, fmt.Errorf("parse OpenClaw config: %w", err)
		}
	}
	openclawcompat.Normalize81(config)
	if m.cfg.OpenClawVersion == "2026.10.2" {
		plan, err := m.planManagedPlugins(filepath.Join(workspace, "home/.openclaw"), "/defaults/.openclaw")
		if err != nil {
			return result, err
		}
		result.ManagedPluginPlanSHA256 = plan.SHA256
	}
	probeRoot, probeHome, stateBytes, err := m.createUpgradeCompatibilityProbe(workspace, config, req)
	if err != nil {
		return result, err
	}
	defer m.removeUpgradeProbe(probeRoot)
	probeConfigPath := filepath.Join(probeHome, ".openclaw", "openclaw.json")
	probeHash := sha256.New()
	if m.cfg.OpenClawVersion == "2026.10.2" {
		if err := m.runOpenClawCommandInHome(ctx, probeRoot, probeHome, probeConfigPath, req, "probe-plugin-metadata", []string{filepath.Join(workspace, "home", ".openclaw"), filepath.Join(probeHome, ".openclaw")}, probeHash, 2*time.Minute); err != nil {
			return result, err
		}
	}
	if err := m.runOpenClawDoctorRepair(ctx, probeRoot, probeHome, probeConfigPath, req, probeHash); err != nil {
		if errors.Is(err, errUpgradeProbeBusy) {
			result.Status = "deferred"
			result.CheckedAt = time.Now().UTC()
			return result, nil
		}
		return result, err
	}
	result.DoctorValidated = true
	if err := m.runOpenClawCommandInHome(ctx, probeRoot, probeHome, probeConfigPath, req, "config-validate", []string{"config", "validate", "--json"}, probeHash, 2*time.Minute); err != nil {
		return result, err
	}
	result.ConfigValidated = true
	if err := m.runOpenClawCommandInHome(ctx, probeRoot, probeHome, probeConfigPath, req, "dry-run", sessionSQLiteMigrationCommands()[0], probeHash, 10*time.Minute); err != nil {
		if errors.Is(err, errUpgradeProbeBusy) {
			result.Status = "deferred"
			result.CheckedAt = time.Now().UTC()
			return result, nil
		}
		return result, err
	}
	result.SessionDryRunValid = true
	target, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, err
	}
	target = append(target, '\n')
	originalHash := sha256.Sum256(raw)
	targetHash := sha256.Sum256(target)
	result.Status = "compatible"
	result.ConfigOriginalSHA256 = hex.EncodeToString(originalHash[:])
	result.ConfigTargetSHA256 = hex.EncodeToString(targetHash[:])
	result.ConfigBytes = int64(len(raw))
	result.SessionBytes = inventory.TotalBytes
	result.StateBytes = stateBytes
	result.AvailableBytes = inventory.AvailableBytes
	result.ProbeOutputSHA256 = hex.EncodeToString(probeHash.Sum(nil))
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func (m *upgradeService) createUpgradeCompatibilityProbe(workspace string, sourceConfig map[string]any, req WorkspaceUpgradeRequest) (string, string, int64, error) {
	// Rehearse on the same PVC in the private operation capsule. This permits
	// a writable probe mount while the live instance remains read-only.
	probeRoot, err := m.io.MkdirTemp(m.io.controlRoot(), fmt.Sprintf("clawmanager-upgrade-%d-", req.InstanceID))
	if err != nil {
		return "", "", 0, err
	}
	ok := false
	defer func() {
		if !ok {
			m.removeUpgradeProbe(probeRoot)
		}
	}()
	probeHome := filepath.Join(probeRoot, "home")
	probeOpenClaw := filepath.Join(probeHome, ".openclaw")
	for _, path := range []string{probeOpenClaw, filepath.Join(probeRoot, "tmp"), filepath.Join(probeRoot, "cache"), filepath.Join(probeRoot, "xdg-state")} {
		if err := m.io.MkdirAll(path, 0o700); err != nil {
			return "", "", 0, err
		}
	}

	// Clone before remapping: the target hash must describe the real normalized
	// configuration, not the sandbox-only path substitutions.
	rawConfig, err := json.Marshal(sourceConfig)
	if err != nil {
		return "", "", 0, err
	}
	probeConfig := map[string]any{}
	if err := json.Unmarshal(rawConfig, &probeConfig); err != nil {
		return "", "", 0, err
	}
	workspaceCopies := remapProbeWorkspacePaths(probeConfig, probeOpenClaw)
	// Session/Agent database paths can be absolute even within the managed HOME.
	// Preserve their layout in the cloned state instead of letting Doctor's
	// isolated HOME still refer to the live writer-owned database.
	if err := remapProbeControlPaths(probeConfig, filepath.Join(workspace, "home", ".openclaw"), probeOpenClaw); err != nil {
		return "", "", 0, err
	}
	workspaceCopies[filepath.Join(workspace, "home", ".openclaw", "workspace")] = filepath.Join(probeOpenClaw, "workspace")
	probeConfigRaw, err := json.MarshalIndent(probeConfig, "", "  ")
	if err != nil {
		return "", "", 0, err
	}
	probeConfigRaw = append(probeConfigRaw, '\n')
	if err := m.io.AtomicWriteFile(filepath.Join(probeOpenClaw, "openclaw.json"), probeConfigRaw, 0o600); err != nil {
		return "", "", 0, err
	}

	sourceOpenClaw := filepath.Join(workspace, "home", ".openclaw")
	var stateBytes int64
	err = m.io.WalkDir(sourceOpenClaw, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		rel, relErr := filepath.Rel(sourceOpenClaw, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrWorkspacePath
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		root := strings.SplitN(slash, "/", 2)[0]
		if slash == "openclaw.json" {
			return nil
		}
		if upgradeProbeExcludedRoot(root) {
			if entry.IsDir() && slash == root {
				return filepath.SkipDir
			}
			return nil
		}
		isState := root == "state"
		isControl := isState || root == "cron" || root == "tasks" || root == "automation" || root == "automations"
		isSupport := root == "extensions" || root == "npm" || root == "plugin-skills"
		isSession := sessionMigrationDirectory(slash) || sessionMigrationFile(slash)
		// Copy every remaining control surface by default.  The previous
		// hand-maintained allowlist omitted top-level exec-approvals.json, so a
		// probe could pass and the identical live Doctor command could fail only
		// after the source gateway had stopped.  Large runtime-only roots are the
		// explicit exclusions above; unknown future control inputs are included.
		wanted := isControl || isSupport || isSession || root != "workspace"
		if entry.IsDir() {
			if !wanted {
				return filepath.SkipDir
			}
			return m.io.MkdirAll(filepath.Join(probeOpenClaw, rel), 0o700)
		}
		if !wanted {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if isControl || isSession {
				return fmt.Errorf("upgrade probe input %s is a symlink", slash)
			}
			if isSupport {
				resolved, err := m.upgradePluginLink(path, sourceOpenClaw)
				if err != nil {
					return fmt.Errorf("resolve probe plugin link %s: %w", slash, err)
				}
				target := resolved
				if pathWithin(sourceOpenClaw, resolved) {
					relative, err := filepath.Rel(sourceOpenClaw, resolved)
					if err != nil {
						return err
					}
					target = filepath.Join(probeOpenClaw, relative)
				} else if !pathWithin("/defaults/.openclaw", resolved) && !pathWithin("/usr/local/lib/node_modules/openclaw", resolved) {
					return fmt.Errorf("upgrade probe plugin link %s leaves managed and image-owned roots", slash)
				}
				return m.io.Symlink(target, filepath.Join(probeOpenClaw, rel))
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := m.copyRegularFileStable(path, filepath.Join(probeOpenClaw, rel), 0o600); err != nil {
			return err
		}
		// StateBytes is also used for probe capacity planning.  Count every
		// copied Doctor input, including top-level migration files such as
		// exec-approvals.json, rather than only the historical allowlist.
		stateBytes += info.Size()
		return nil
	})
	if err != nil {
		return "", "", 0, err
	}
	if req.RecoveryMode == "rebuild" || m.cfg.OpenClawVersion == "2026.10.2" {
		if err := m.rebuildImageManagedPlugins(probeOpenClaw, "/defaults/.openclaw"); err != nil {
			return "", "", 0, err
		}
	} else if err := m.validateUpgradePluginLinks(probeOpenClaw); err != nil {
		return "", "", 0, err
	}
	for source, target := range workspaceCopies {
		if err := m.io.MkdirAll(target, 0o700); err != nil {
			return "", "", 0, err
		}
		for _, name := range []string{"HEARTBEAT.md", "TOOLS.md"} {
			sourcePath := filepath.Join(source, name)
			if info, statErr := m.io.Lstat(sourcePath); statErr == nil && info.Mode().IsRegular() {
				if err := m.copyRegularFileStable(sourcePath, filepath.Join(target, name), 0o600); err != nil {
					return "", "", 0, err
				}
			}
		}
	}
	if err := m.chownTree(probeRoot, req.UID, req.GID); err != nil {
		return "", "", 0, err
	}
	ok = true
	return probeRoot, probeHome, stateBytes, nil
}

func upgradeProbeExcludedRoot(root string) bool {
	switch strings.ToLower(strings.TrimSpace(root)) {
	case "workspace", "tmp", "cache", "xdg-state", "browser", "canvas", "media", "sandboxes":
		return true
	default:
		return false
	}
}

func remapProbeWorkspacePaths(config map[string]any, probeOpenClaw string) map[string]string {
	copies := map[string]string{}
	var walk func(any, string)
	walk = func(value any, key string) {
		switch typed := value.(type) {
		case map[string]any:
			for childKey, child := range typed {
				lower := strings.ToLower(strings.TrimSpace(childKey))
				if text, ok := child.(string); ok && (lower == "workspace" || lower == "workspacepath" || lower == "workspace_path") {
					digest := sha256.Sum256([]byte(text))
					target := filepath.Join(probeOpenClaw, "workspace-probes", hex.EncodeToString(digest[:6]))
					typed[childKey] = target
					if filepath.IsAbs(strings.TrimSpace(text)) {
						copies[filepath.Clean(text)] = target
					}
					continue
				}
				walk(child, childKey)
			}
		case []any:
			for _, child := range typed {
				walk(child, key)
			}
		}
	}
	walk(config, "")
	return copies
}

func remapProbeControlPaths(config map[string]any, sourceState, probeState string) error {
	remap := func(raw string) (string, error) {
		resolved := strings.TrimSpace(raw)
		if resolved == "" {
			return raw, nil
		}
		if strings.HasPrefix(resolved, "~/") || strings.HasPrefix(resolved, "~\\") {
			resolved = filepath.Join(filepath.Dir(sourceState), resolved[2:])
		} else if !filepath.IsAbs(resolved) {
			// Native resolveUserPath uses cwd for non-tilde relative paths.
			// Upgrade commands run from the instance workspace, not HOME.
			resolved = filepath.Join(filepath.Dir(filepath.Dir(sourceState)), resolved)
		}
		resolved = filepath.Clean(resolved)
		if !pathWithin(sourceState, resolved) {
			return "", errors.New("upgrade preflight cannot isolate configured control state outside the persistent .openclaw directory")
		}
		rel, err := filepath.Rel(sourceState, resolved)
		if err != nil {
			return "", err
		}
		return filepath.Join(probeState, rel), nil
	}
	var walk func(any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "agentDir" {
					if raw, ok := child.(string); ok {
						mapped, err := remap(raw)
						if err != nil {
							return err
						}
						typed[key] = mapped
					}
				} else if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(config["agents"]); err != nil {
		return err
	}
	if session, ok := config["session"].(map[string]any); ok {
		if raw, ok := session["store"].(string); ok {
			mapped, err := remap(raw)
			if err != nil {
				return err
			}
			session["store"] = mapped
		}
	}
	if plugins, ok := config["plugins"].(map[string]any); ok {
		remapPlugin := func(raw string) (string, error) {
			if strings.HasPrefix(filepath.ToSlash(raw), "/") && (pathWithin("/defaults/.openclaw", raw) || pathWithin("/usr/local/lib/node_modules/openclaw", raw)) {
				return raw, nil
			}
			return remap(raw)
		}
		if load, ok := plugins["load"].(map[string]any); ok {
			if paths, ok := load["paths"].([]any); ok {
				for i, value := range paths {
					if raw, ok := value.(string); ok {
						mapped, err := remapPlugin(raw)
						if err != nil {
							return err
						}
						paths[i] = mapped
					}
				}
			}
		}
		if installs, ok := plugins["installs"].(map[string]any); ok {
			for _, value := range installs {
				if record, ok := value.(map[string]any); ok {
					if raw, ok := record["installPath"].(string); ok {
						mapped, err := remapPlugin(raw)
						if err != nil {
							return err
						}
						record["installPath"] = mapped
					}
				}
			}
		}
	}
	return nil
}

func (m *upgradeService) copyRegularFileStable(source, target string, mode os.FileMode) error {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := m.io.Lstat(source)
		if err != nil {
			return err
		}
		if !before.Mode().IsRegular() {
			return fmt.Errorf("upgrade probe source is not a regular file: %s", source)
		}
		if err := m.io.CopyRegularFile(source, target, mode); err != nil {
			return err
		}
		after, err := m.io.Lstat(source)
		if err != nil {
			return err
		}
		if before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) {
			return nil
		}
		_ = m.io.Remove(target)
	}
	return errors.New("upgrade probe source remained active; retry after current write completes")
}

func (m *upgradeService) chownTree(root string, uid, gid int) error {
	return m.io.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return m.io.Chown(path, uid, gid)
	})
}

func (m *upgradeService) removeUpgradeProbe(path string) {
	clean := filepath.Clean(path)
	if strings.HasPrefix(filepath.Base(clean), "clawmanager-upgrade-") && filepath.Dir(clean) == m.io.controlRoot() {
		_ = m.io.RemoveAll(clean)
	}
}

func (m *upgradeService) runOpenClawCommandInHome(ctx context.Context, dir, home, configPath string, req WorkspaceUpgradeRequest, phase string, args []string, hash io.Writer, timeout time.Duration) error {
	phaseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stateDir := filepath.Join(home, ".openclaw")
	tempRoot := filepath.Join(stateDir, "tmp")
	scratchRoot := filepath.Join(tempRoot, "clawmanager-upgrade-"+req.RolloutID)
	cacheRoot := filepath.Join(stateDir, "cache")
	xdgStateRoot := filepath.Join(stateDir, "xdg-state")
	for _, path := range []string{tempRoot, scratchRoot, cacheRoot, xdgStateRoot} {
		if err := m.io.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := m.io.Chown(path, req.UID, req.GID); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(phaseCtx, "openclaw", args...)
	if phase == "probe-plugin-metadata" {
		cmd = exec.CommandContext(phaseCtx, "node", append([]string{"/usr/local/share/openclaw/patches/isolate_upgrade_plugin_metadata_102.mjs"}, args...)...)
	}
	if phase == "managed-plugin-metadata" {
		cmd = exec.CommandContext(phaseCtx, "node", "/usr/local/share/openclaw/patches/converge_plugin_records_102.mjs")
	}
	cmd.Dir = dir
	overrides := map[string]string{
		"HOME": home, "OPENCLAW_STATE_DIR": stateDir, "OPENCLAW_CONFIG_PATH": configPath,
		"TMPDIR": scratchRoot, "XDG_CACHE_HOME": cacheRoot,
		"XDG_STATE_HOME": xdgStateRoot, "NO_COLOR": "1",
		"OPENCLAW_NO_RESPAWN": "1",
	}
	// Plugin generation copies can be large and exist only for this command.
	// Keep durable captures/config/history on their original NFS filesystem.
	// The pinned core overlay admits only this UID-owned, private local root.
	if runtime.GOOS == "linux" {
		pluginScratch, err := m.io.MkdirTemp("/tmp", "clawmanager-upgrade-plugin-")
		if err != nil {
			return err
		}
		defer m.io.RemoveAll(pluginScratch)
		if err := m.io.Chown(pluginScratch, req.UID, req.GID); err != nil {
			return err
		}
		overrides["CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT"] = pluginScratch
	}
	// OpenClaw 2026.8.1 otherwise asks Doctor to migrate legacy exec
	// approvals while blocking Doctor itself at the same migration gate.  The
	// image patch accepts this authority only for an explicit, non-interactive
	// Doctor repair command; ordinary commands remain fail-closed.
	if isOpenClawDoctorRepairCommand(args) {
		overrides["CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION"] = "1"
	}
	cmd.Env = m.upgradeCommandEnv(overrides)
	configureGatewayCommand(cmd, req.UID, req.GID)
	if !m.cfg.IsolatedLifecycle {
		inheritUpgradeOperationFence(ctx, cmd)
		cleanup := fenceUpgradeCommand(cmd)
		defer cleanup()
	}
	output, runErr := m.combinedUpgradeOutput(phaseCtx, cmd, req)
	if len(output) > 64*1024 {
		output = output[:64*1024]
	}
	_, _ = hash.Write([]byte(phase))
	_, _ = hash.Write(output)
	if runErr == nil {
		return nil
	}
	digest := sha256.Sum256(output)
	failure := fmt.Errorf("OPENCLAW_UPGRADE_%s_FAILED: %s (output_sha256=%x): %w", strings.ToUpper(strings.ReplaceAll(phase, "-", "_")), safeOpenClawFailureSummary(output), digest, runErr)
	lower := strings.ToLower(string(output))
	if strings.Contains(lower, "database is locked") || strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "resource temporarily unavailable") {
		return errors.Join(errUpgradeProbeBusy, failure)
	}
	if m.io != nil && isDeferredMetadataSchemaError(phaseCtx, phase, output, runErr, m.io.commandUncertain) {
		return errors.Join(errUpgradeStateSchemaRequired, failure)
	}
	return failure
}

func isDeferredMetadataSchemaError(ctx context.Context, phase string, output []byte, runErr error, uncertain bool) bool {
	// Only an ordinary CLI rejection can defer to Doctor. Joined teardown,
	// controller and lease failures must never authorize another command.
	_, exited := runErr.(*exec.ExitError)
	return exited && ctx.Err() == nil && !uncertain && phase == "managed-plugin-metadata" &&
		bytes.Contains(output, []byte("OpenClawStateDatabaseSchemaMigrationRequiredError: OpenClaw state database schema migration required (audit-events-v2)")) &&
		bytes.Contains(output, []byte("run openclaw doctor --fix to migrate it"))
}

func isOpenClawDoctorRepairCommand(args []string) bool {
	if len(args) == 0 || args[0] != "doctor" || !containsArgument(args, "--non-interactive") {
		return false
	}
	return containsArgument(args, "--fix") || containsArgument(args, "--repair")
}

func containsArgument(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

// runOpenClawDoctorRepair permits bounded convergence because OpenClaw can
// repair the shared SQLite schema before it can discover the next generation
// of Doctor-owned state.  A retry is allowed only when durable migration
// inputs changed; a repeated failure with the same fingerprint fails closed.
func (m *upgradeService) runOpenClawDoctorRepair(ctx context.Context, dir, home, configPath string, req WorkspaceUpgradeRequest, hash io.Writer) error {
	_, markerErr := m.io.Stat(filepath.Join(home, ".openclaw/.clawmanager-plugin-convergence.json"))
	metadataPending := markerErr == nil
	const maxPasses = 4
	args := []string{"doctor", "--fix", "--yes", "--non-interactive"}
	for pass := 1; pass <= maxPasses; pass++ {
		var metadataErr error
		if metadataPending {
			metadataErr = m.runOpenClawCommandInHome(ctx, dir, home, configPath, req, "managed-plugin-metadata", nil, hash, 2*time.Minute)
			if metadataErr == nil {
				metadataPending = false
			} else if !errors.Is(metadataErr, errUpgradeStateSchemaRequired) {
				return metadataErr
			}
		}
		before, err := m.openClawDoctorMigrationFingerprint(home, configPath)
		if err != nil {
			return fmt.Errorf("OPENCLAW_UPGRADE_DOCTOR_FINGERPRINT_FAILED: %w", err)
		}
		err = m.runOpenClawCommandInHome(ctx, dir, home, configPath, req, fmt.Sprintf("doctor-fix-pass-%d", pass), args, hash, 10*time.Minute)
		if err == nil && !metadataPending {
			return nil
		}
		if errors.Is(err, errUpgradeProbeBusy) {
			return err
		}
		// Only the exact old-schema metadata failure may defer to official
		// Doctor. A progressing pass retries metadata before the final Doctor.
		err = errors.Join(err, metadataErr)
		after, fingerprintErr := m.openClawDoctorMigrationFingerprint(home, configPath)
		if fingerprintErr != nil {
			return errors.Join(err, fmt.Errorf("OPENCLAW_UPGRADE_DOCTOR_FINGERPRINT_FAILED: %w", fingerprintErr))
		}
		if after == before {
			return errors.Join(err, errors.New("OPENCLAW_UPGRADE_DOCTOR_NO_PROGRESS: Doctor failed without changing any migration input"))
		}
		if pass == maxPasses {
			return errors.Join(err, fmt.Errorf("OPENCLAW_UPGRADE_DOCTOR_DID_NOT_CONVERGE: migration still failed after %d progressing passes", maxPasses))
		}
	}
	return errors.New("OPENCLAW_UPGRADE_DOCTOR_DID_NOT_CONVERGE")
}

func (m *upgradeService) openClawDoctorMigrationFingerprint(home, configPath string) (string, error) {
	hash := sha256.New()
	for _, path := range []string{
		configPath,
		filepath.Join(home, ".openclaw", "exec-approvals.json"),
		filepath.Join(home, ".openclaw", "exec-approvals.json.doctor-importing"),
		filepath.Join(home, ".openclaw", "identity", "device.json"),
		filepath.Join(home, ".openclaw", "state", "openclaw.sqlite"),
	} {
		info, err := m.io.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = io.WriteString(hash, path+"\x00missing\n")
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("Doctor migration input is not a regular file: %s", path)
		}
		file, err := m.io.Open(path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, path+"\x00"+strconv.FormatInt(info.Size(), 10)+"\x00")
		if filepath.Base(path) == "openclaw.sqlite" {
			// Header fields include the schema cookie and user_version without
			// hashing a potentially large database on every failed pass.
			header := make([]byte, 100)
			n, readErr := io.ReadFull(file, header)
			if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
				_ = file.Close()
				return "", readErr
			}
			_, _ = hash.Write(header[:n])
		} else if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *upgradeService) upgradeCommandEnv(overrides map[string]string) []string {
	var base []string
	if m.cfg.IsolatedLifecycle {
		base = m.cfg.GatewayBaseEnv
	} else {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			env = append(env, item)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

// MigrateSessionSQLite executes OpenClaw's public, transactional migration
// workflow against the fixed persistent HOME. It is only callable while the
// gateway is stopped and the control plane owns the workspace writer lease.
func (m *upgradeService) MigrateSessionSQLite(ctx context.Context, req WorkspaceUpgradeRequest) (result SessionSQLiteMigrationResult, resultErr error) {
	closeIO, ioErr := m.beginMutationIO(req)
	if ioErr != nil {
		return SessionSQLiteMigrationResult{}, ioErr
	}
	defer func() { resultErr = errors.Join(resultErr, closeIO()) }()

	result = SessionSQLiteMigrationResult{InstanceID: req.InstanceID, RolloutID: req.RolloutID, Generation: req.Generation, PhaseDurationsMS: map[string]int64{}}
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return result, err
	}
	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return result, err
	}
	unlock, err := m.host.LockUpgradeOperation(req.InstanceID)
	if err != nil {
		return result, err
	}
	defer unlock()
	ctx = m.host.UpgradeOperationContext(ctx, req.InstanceID)
	if !IsOpenClawAtLeast(m.cfg.OpenClawVersion, OpenClaw81Version) {
		return result, fmt.Errorf("OpenClaw %s does not support session SQLite migration", m.cfg.OpenClawVersion)
	}
	if m.host.InstanceActive(req.InstanceID) {
		return result, fmt.Errorf("instance %d still has an active gateway", req.InstanceID)
	}
	ctx, finishLease, err := m.host.KeepUpgradeWriterLease(ctx, req)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, finishLease()) }()
	if req.UID <= 0 || req.GID <= 0 {
		return result, errors.New("positive uid and gid are required for session migration")
	}
	receiptPath := m.sessionMigrationReceiptPath(req)
	if raw, readErr := m.io.ReadFile(receiptPath); readErr == nil {
		var existing SessionSQLiteMigrationResult
		if json.Unmarshal(raw, &existing) != nil || !sessionMigrationReceiptMatches(existing, req) {
			return result, errors.New("migration receipt belongs to another execution or is invalid")
		}
		if existing.Status == "validated" {
			return existing, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	}
	home := filepath.Join(workspace, "home")
	if info, err := m.io.Stat(home); err != nil || !info.IsDir() {
		return result, fmt.Errorf("persistent HOME is unavailable: %w", err)
	}
	plan, planErr := m.planManagedPlugins(filepath.Join(home, ".openclaw"), "/defaults/.openclaw")
	if planErr != nil {
		return result, planErr
	}
	if req.ManagedPluginPlanSHA256 != "" && req.ManagedPluginPlanSHA256 != plan.SHA256 {
		return result, errors.New("OPENCLAW_PLUGIN_PLAN_CHANGED: plugin inputs changed since compatibility preflight")
	}
	// Preserve original control/plugin state BEFORE installing any repaired candidate.
	stateCapsule, err := m.prepareOpenClawStateCapsule(workspace, req)
	if err != nil {
		return result, err
	}
	result.StateCapsuleBytes = stateCapsule.TotalBytes
	if req.RecoveryMode == "rebuild" || (m.cfg.OpenClawVersion == "2026.10.2" && len(plan.Changes) > 0) {
		if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
			return result, err
		}
		proof, err := m.rebuildPreservingState(ctx, workspace, req)
		if err != nil {
			return result, err
		}
		result.DataPreserved, result.PreservationSHA256 = proof.Installed, proof.OriginalSHA256
	} else if req.RecoveryMode != "" && req.RecoveryMode != "retry" {
		return result, errors.New("unsupported upgrade recovery action")
	}
	// Inspect the current validated state before preparing/normalizing config.
	// A forward retry must preserve legitimate writes since the prior migration.
	if req.RecoveryMode == "retry" && req.ResumeRolloutID != "" {
		if reused, ok, err := m.resumeValidatedMigration(ctx, workspace, req, result); err != nil {
			return result, err
		} else if ok {
			return reused, nil
		}
	}
	capsule, err := m.prepareOpenClaw81Config(workspace, req)
	if err != nil {
		return result, err
	}
	result.ConfigOriginalSHA256 = capsule.OriginalSHA256
	result.ConfigTargetSHA256 = capsule.TargetSHA256

	hash := sha256.New()
	phaseStarted := time.Now()
	if err := m.runOpenClawDoctorRepair(ctx, workspace, home, filepath.Join(home, ".openclaw", "openclaw.json"), req, hash); err != nil {
		result.PhaseDurationsMS["doctor_fix"] = time.Since(phaseStarted).Milliseconds()
		result.Status = "failed_doctor_fix"
		result.OutputSHA256 = hex.EncodeToString(hash.Sum(nil))
		result.CompletedAt = time.Now().UTC()
		if receiptErr := m.writeSessionMigrationReceipt(receiptPath, result); receiptErr != nil {
			return result, errors.Join(err, fmt.Errorf("persist failed migration receipt: %w", receiptErr))
		}
		return result, err
	}
	result.PhaseDurationsMS["doctor_fix"] = time.Since(phaseStarted).Milliseconds()
	phaseStarted = time.Now()
	if err := m.runOpenClawUpgradeCommand(ctx, workspace, home, req, "config-validate", []string{"config", "validate", "--json"}, hash); err != nil {
		result.PhaseDurationsMS["config_validate"] = time.Since(phaseStarted).Milliseconds()
		result.Status = "failed_config_validate"
		result.OutputSHA256 = hex.EncodeToString(hash.Sum(nil))
		result.CompletedAt = time.Now().UTC()
		if receiptErr := m.writeSessionMigrationReceipt(receiptPath, result); receiptErr != nil {
			return result, errors.Join(err, fmt.Errorf("persist failed migration receipt: %w", receiptErr))
		}
		return result, err
	}
	result.PhaseDurationsMS["config_validate"] = time.Since(phaseStarted).Milliseconds()
	for _, args := range sessionSQLiteMigrationCommands() {
		if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
			return result, err
		}
		phase := args[2]
		phaseStarted = time.Now()
		if err := m.runOpenClawUpgradeCommand(ctx, workspace, home, req, phase, args, hash); err != nil {
			result.PhaseDurationsMS[strings.ReplaceAll(phase, "-", "_")] = time.Since(phaseStarted).Milliseconds()
			result.Status = "failed_" + strings.ReplaceAll(phase, "-", "_")
			result.OutputSHA256 = hex.EncodeToString(hash.Sum(nil))
			result.ArchiveBytes, result.ArchiveFiles, _ = m.sessionMigrationArchiveStats(home)
			result.RollbackAvailable = result.ArchiveFiles > 0
			result.CompletedAt = time.Now().UTC()
			if receiptErr := m.writeSessionMigrationReceipt(receiptPath, result); receiptErr != nil {
				return result, errors.Join(err, fmt.Errorf("persist failed migration receipt: %w", receiptErr))
			}
			return result, err
		}
		result.PhaseDurationsMS[strings.ReplaceAll(phase, "-", "_")] = time.Since(phaseStarted).Milliseconds()
	}
	result.Status = "validated"
	result.OutputSHA256 = hex.EncodeToString(hash.Sum(nil))
	result.ArchiveBytes, result.ArchiveFiles, err = m.sessionMigrationArchiveStats(home)
	if err != nil {
		return result, err
	}
	result.RollbackAvailable = result.ArchiveFiles > 0
	phaseStarted = time.Now()
	result.SessionCount, result.SessionCatalogSHA256, err = m.inspectOfficialSessionCatalog(ctx, workspace, home, req)
	result.PhaseDurationsMS["session_catalog"] = time.Since(phaseStarted).Milliseconds()
	if err != nil {
		result.Status = "failed_session_catalog"
		result.CompletedAt = time.Now().UTC()
		if receiptErr := m.writeSessionMigrationReceipt(receiptPath, result); receiptErr != nil {
			return result, errors.Join(err, fmt.Errorf("persist failed migration receipt: %w", receiptErr))
		}
		return result, err
	}
	if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
		return result, err
	}
	result.CompletedAt = time.Now().UTC()
	if err := m.writeSessionMigrationReceipt(receiptPath, result); err != nil {
		return result, err
	}
	return result, nil
}

// SessionSQLiteMigrationStatus returns only the durable receipt written by the
// migration operation. It never starts or repeats a migration, which lets the
// control plane safely reconcile a lost HTTP response.
func (m *upgradeService) SessionSQLiteMigrationStatus(req WorkspaceUpgradeRequest) (SessionSQLiteMigrationResult, error) {
	closeIO, ioErr := m.beginIO(req, false)
	if ioErr != nil {
		return SessionSQLiteMigrationResult{}, ioErr
	}
	defer closeIO()

	var result SessionSQLiteMigrationResult
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return result, err
	}
	if _, err := m.host.WorkspaceForUpgrade(req); err != nil {
		return result, err
	}
	raw, err := m.io.ReadFile(m.sessionMigrationReceiptPath(req))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("invalid session migration receipt: %w", err)
	}
	if !sessionMigrationReceiptMatches(result, req) || strings.TrimSpace(result.Status) == "" {
		return result, errors.New("session migration receipt identity is invalid")
	}
	return result, nil
}

func sessionMigrationReceiptMatches(result SessionSQLiteMigrationResult, req WorkspaceUpgradeRequest) bool {
	return result.InstanceID == req.InstanceID && (result.RolloutID == "" || result.RolloutID == req.RolloutID) && (result.Generation == 0 || result.Generation == req.Generation)
}

func (m *upgradeService) inspectOfficialSessionCatalog(ctx context.Context, workspace, home string, req WorkspaceUpgradeRequest) (int, string, error) {
	phaseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stateDir := filepath.Join(home, ".openclaw")
	cmd := exec.CommandContext(phaseCtx, "openclaw", "sessions", "--all-agents", "--json", "--limit", "all")
	cmd.Dir = workspace
	cmd.Env = m.upgradeCommandEnv(map[string]string{
		"HOME": home, "OPENCLAW_STATE_DIR": stateDir,
		"OPENCLAW_CONFIG_PATH": filepath.Join(stateDir, "openclaw.json"), "NO_COLOR": "1",
	})
	configureGatewayCommand(cmd, req.UID, req.GID)
	if !m.cfg.IsolatedLifecycle {
		inheritUpgradeOperationFence(ctx, cmd)
		cleanup := fenceUpgradeCommand(cmd)
		defer cleanup()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := m.runUpgradeCommand(phaseCtx, cmd, req)
	if err != nil {
		output := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
		digest := sha256.Sum256(output)
		return 0, "", fmt.Errorf("OPENCLAW_UPGRADE_SESSION_CATALOG_FAILED: %s (output_sha256=%x): %w", safeOpenClawFailureSummary(output), digest, err)
	}
	count, digest, parseErr := canonicalSessionCatalog(stdout.Bytes())
	if parseErr == nil {
		return count, digest, nil
	}
	output := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	outputDigest := sha256.Sum256(output)
	return 0, "", fmt.Errorf("OPENCLAW_UPGRADE_SESSION_CATALOG_INVALID: output_sha256=%x: %w", outputDigest, parseErr)
}

func canonicalSessionCatalog(raw []byte) (int, string, error) {
	catalogs := make([]openClawSessionCatalog, 0, 1)
	for offset := 0; offset < len(raw); {
		start := bytes.IndexByte(raw[offset:], '{')
		if start < 0 {
			break
		}
		start += offset
		decoder := json.NewDecoder(bytes.NewReader(raw[start:]))
		var candidate map[string]json.RawMessage
		if err := decoder.Decode(&candidate); err != nil {
			offset = start + 1
			continue
		}
		consumed := int(decoder.InputOffset())
		if _, ok := candidate["sessions"]; ok {
			encoded, err := json.Marshal(candidate)
			if err != nil {
				return 0, "", fmt.Errorf("OPENCLAW_UPGRADE_SESSION_CATALOG_INVALID: %w", err)
			}
			var catalog openClawSessionCatalog
			if err := json.Unmarshal(encoded, &catalog); err != nil {
				return 0, "", fmt.Errorf("OPENCLAW_UPGRADE_SESSION_CATALOG_INVALID: %w", err)
			}
			catalogs = append(catalogs, catalog)
		}
		if consumed <= 0 {
			offset = start + 1
		} else {
			offset = start + consumed
		}
	}
	if len(catalogs) != 1 {
		return 0, "", fmt.Errorf("OPENCLAW_UPGRADE_SESSION_CATALOG_INVALID: expected exactly one session catalog, found %d", len(catalogs))
	}
	catalog := catalogs[0]
	entries := make([]string, 0, len(catalog.Sessions))
	for _, session := range catalog.Sessions {
		key, sessionID := strings.TrimSpace(session.Key), strings.TrimSpace(session.SessionID)
		if key == "" || sessionID == "" {
			return 0, "", errors.New("OPENCLAW_UPGRADE_SESSION_CATALOG_INVALID: session key or id is empty")
		}
		entries = append(entries, key+"\x00"+sessionID+"\n")
	}
	sort.Strings(entries)
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = io.WriteString(hash, entry)
	}
	return len(entries), hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *upgradeService) writeSessionMigrationReceipt(path string, result SessionSQLiteMigrationResult) error {
	if err := m.io.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(result)
	return m.io.AtomicWriteFile(path, append(raw, '\n'), 0o600)
}

func (m *upgradeService) sessionMigrationReceiptPath(req WorkspaceUpgradeRequest) string {
	return filepath.Join(m.host.UpgradeRoot(), "rollout-"+req.RolloutID, "instance-"+strconv.Itoa(req.InstanceID), "session-sqlite-migration.json")
}

func (m *upgradeService) sessionRestoreReceiptPath(req WorkspaceUpgradeRequest) string {
	return filepath.Join(m.host.UpgradeRoot(), "rollout-"+req.RolloutID, "instance-"+strconv.Itoa(req.InstanceID), "session-sqlite-restore.json")
}

// RestoreSessionSQLite invokes OpenClaw's supported rollback workflow. It only
// restores the upstream migration archive; project files and the rest of the
// persistent workspace are never copied or replaced.
func (m *upgradeService) RestoreSessionSQLite(ctx context.Context, req WorkspaceUpgradeRequest) (result SessionSQLiteRestoreResult, resultErr error) {
	closeIO, ioErr := m.beginMutationIO(req)
	if ioErr != nil {
		return SessionSQLiteRestoreResult{}, ioErr
	}
	defer func() { resultErr = errors.Join(resultErr, closeIO()) }()

	result = SessionSQLiteRestoreResult{InstanceID: req.InstanceID, RolloutID: req.RolloutID, Generation: req.Generation, PhaseDurationsMS: map[string]int64{}}
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return result, err
	}
	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return result, err
	}
	unlock, err := m.host.LockUpgradeOperation(req.InstanceID)
	if err != nil {
		return result, err
	}
	defer unlock()
	ctx = m.host.UpgradeOperationContext(ctx, req.InstanceID)
	if !IsOpenClawAtLeast(m.cfg.OpenClawVersion, OpenClaw81Version) {
		return result, fmt.Errorf("OpenClaw %s does not support session SQLite restore", m.cfg.OpenClawVersion)
	}
	if m.host.InstanceActive(req.InstanceID) {
		return result, fmt.Errorf("instance %d still has an active gateway", req.InstanceID)
	}
	ctx, finishLease, err := m.host.KeepUpgradeWriterLease(ctx, req)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, finishLease()) }()
	if req.UID <= 0 || req.GID <= 0 {
		return result, errors.New("positive uid and gid are required for session restore")
	}
	receiptPath := m.sessionRestoreReceiptPath(req)
	if raw, readErr := m.io.ReadFile(receiptPath); readErr == nil {
		var existing SessionSQLiteRestoreResult
		if json.Unmarshal(raw, &existing) != nil || !sessionRestoreReceiptMatches(existing, req) {
			return result, errors.New("restore receipt belongs to another execution or is invalid")
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	}
	migration := SessionSQLiteMigrationResult{}
	raw, migrationReadErr := m.io.ReadFile(m.sessionMigrationReceiptPath(req))
	home := filepath.Join(workspace, "home")
	hasMigrationArchive := !req.PreserveSessionSQLite && migrationReadErr == nil && json.Unmarshal(raw, &migration) == nil && migration.RollbackAvailable
	// The upstream importer can move JSONL files before a process or storage
	// failure prevents our receipt from being written. Discovering the official
	// archive closes that crash window without copying or guessing user data.
	if !req.PreserveSessionSQLite && !hasMigrationArchive {
		_, archiveFiles, archiveErr := m.sessionMigrationArchiveStats(home)
		if archiveErr != nil {
			return result, fmt.Errorf("inspect session migration archive: %w", archiveErr)
		}
		hasMigrationArchive = archiveFiles > 0
	}
	hash := sha256.New()
	if hasMigrationArchive {
		args := []string{"doctor", "--session-sqlite", "restore", "--session-sqlite-all-agents", "--json", "--non-interactive", "--yes"}
		phaseStarted := time.Now()
		if err := m.runOpenClawUpgradeCommand(ctx, workspace, home, req, "restore", args, hash); err != nil {
			result.PhaseDurationsMS["session_restore"] = time.Since(phaseStarted).Milliseconds()
			return result, err
		}
		result.PhaseDurationsMS["session_restore"] = time.Since(phaseStarted).Milliseconds()
	}
	phaseStarted := time.Now()
	if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
		return result, err
	}
	configRestored, configErr := m.restoreOpenClawConfig(workspace, req)
	result.PhaseDurationsMS["config_restore"] = time.Since(phaseStarted).Milliseconds()
	if configErr != nil {
		return result, configErr
	}
	phaseStarted = time.Now()
	if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
		return result, err
	}
	stateRestored, stateErr := m.restoreOpenClawStateCapsule(workspace, req)
	result.PhaseDurationsMS["state_restore"] = time.Since(phaseStarted).Milliseconds()
	if stateErr != nil {
		return result, stateErr
	}
	if !hasMigrationArchive && !configRestored && !stateRestored {
		return result, errors.New("upgrade rollback capsule is unavailable")
	}
	result.Status = "restored"
	if err := m.host.VerifyUpgradeWriter(ctx, req); err != nil {
		return result, err
	}
	result.OutputSHA256 = hex.EncodeToString(hash.Sum(nil))
	result.ConfigRestored = configRestored
	result.StateRestored = stateRestored
	result.PreservedSessionSQLite = req.PreserveSessionSQLite
	result.CompletedAt = time.Now().UTC()
	if err := m.io.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		return result, err
	}
	raw, _ = json.Marshal(result)
	if err := m.io.AtomicWriteFile(receiptPath, append(raw, '\n'), 0o600); err != nil {
		return result, err
	}
	return result, nil
}

func sessionRestoreReceiptMatches(result SessionSQLiteRestoreResult, req WorkspaceUpgradeRequest) bool {
	return result.InstanceID == req.InstanceID && (result.RolloutID == "" || result.RolloutID == req.RolloutID) && (result.Generation == 0 || result.Generation == req.Generation) && result.Status == "restored" && result.PreservedSessionSQLite == req.PreserveSessionSQLite
}

// SessionSQLiteRestoreStatus is the read-only counterpart of restore. It is
// used only to resolve an ambiguous transport failure.
func (m *upgradeService) SessionSQLiteRestoreStatus(req WorkspaceUpgradeRequest) (SessionSQLiteRestoreResult, error) {
	closeIO, ioErr := m.beginIO(req, false)
	if ioErr != nil {
		return SessionSQLiteRestoreResult{}, ioErr
	}
	defer closeIO()

	var result SessionSQLiteRestoreResult
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return result, err
	}
	if _, err := m.host.WorkspaceForUpgrade(req); err != nil {
		return result, err
	}
	raw, err := m.io.ReadFile(m.sessionRestoreReceiptPath(req))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("invalid session restore receipt: %w", err)
	}
	if !sessionRestoreReceiptMatches(result, req) {
		return result, errors.New("session restore receipt identity is invalid")
	}
	return result, nil
}

func (m *upgradeService) runOpenClawUpgradeCommand(ctx context.Context, workspace, home string, req WorkspaceUpgradeRequest, phase string, args []string, hash io.Writer) error {
	return m.runOpenClawCommandInHome(ctx, workspace, home, filepath.Join(home, ".openclaw", "openclaw.json"), req, phase, args, hash, 10*time.Minute)
}

func safeOpenClawFailureSummary(output []byte) string {
	var safe []string
	// Doctor wraps plugin failures in a box. The reason is on the following
	// indented line and often contains no "error" keyword of its own.
	continuations := 0
	for _, line := range strings.Split(string(output), "\n") {
		boxed := strings.HasPrefix(strings.TrimSpace(line), "│")
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.Trim(line, "│"))
		lower := strings.ToLower(line)
		matches := strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "reason") || strings.Contains(lower, "unrecognized key") || strings.Contains(lower, "invalid config") || strings.Contains(lower, "config validation failed") || strings.Contains(lower, "schema migration") || strings.Contains(lower, "migration required") || strings.Contains(lower, "legacy exec approvals") || strings.Contains(lower, "legacy agent database") || strings.Contains(lower, "database is locked") || strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "transcript_malformed") || strings.Contains(lower, "transcript_missing")
		if line == "" || (!matches && !(boxed && continuations > 0)) {
			continuations = 0
			continue
		}
		if matches && strings.Contains(lower, "post-core payload smoke check") {
			continuations = 3
		} else if continuations > 0 {
			continuations--
		}
		line = regexp.MustCompile(`/[^ ]+`).ReplaceAllString(line, "<path>")
		line = regexp.MustCompile(`(?i)(token|password|secret)([=: ]+)[^,; ]+`).ReplaceAllString(line, "$1$2<redacted>")
		line = regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`).ReplaceAllString(line, "<digest>")
		if len([]rune(line)) > 480 {
			line = string([]rune(line)[:480])
		}
		safe = append(safe, line)
		if len(safe) == 12 {
			break
		}
	}
	if len(safe) == 0 {
		return "target OpenClaw rejected the configuration or session migration input"
	}
	return strings.Join(safe, "; ")
}

func (m *upgradeService) configCapsuleDir(req WorkspaceUpgradeRequest) string {
	return filepath.Join(m.host.UpgradeRoot(), "rollout-"+req.RolloutID, "instance-"+strconv.Itoa(req.InstanceID))
}

func (m *upgradeService) prepareOpenClaw81Config(workspace string, req WorkspaceUpgradeRequest) (upgradeConfigCapsule, error) {
	var capsule upgradeConfigCapsule
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	raw, readErr := m.io.ReadFile(configPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return capsule, fmt.Errorf("read OpenClaw config: %w", readErr)
	}
	if len(raw) > maxUpgradeConfigBytes {
		return capsule, fmt.Errorf("OpenClaw config exceeds %d byte upgrade limit", maxUpgradeConfigBytes)
	}
	originalExists := readErr == nil
	originalMode := uint32(0o600)
	if info, statErr := m.io.Stat(configPath); statErr == nil {
		originalMode = uint32(info.Mode().Perm())
	}
	config := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return capsule, fmt.Errorf("parse OpenClaw config: %w", err)
		}
	}
	openclawcompat.Normalize81(config)
	target, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return capsule, fmt.Errorf("marshal normalized OpenClaw config: %w", err)
	}
	target = append(target, '\n')
	originalHash := sha256.Sum256(raw)
	targetHash := sha256.Sum256(target)
	capsule = upgradeConfigCapsule{SchemaVersion: 1, InstanceID: req.InstanceID, OriginalExists: originalExists, OriginalMode: originalMode, OriginalSHA256: hex.EncodeToString(originalHash[:]), TargetSHA256: hex.EncodeToString(targetHash[:])}
	dir := m.configCapsuleDir(req)
	if err := m.io.MkdirAll(dir, 0o700); err != nil {
		return capsule, err
	}
	manifestPath := filepath.Join(dir, "config-capsule.json")
	if existing, err := m.io.ReadFile(manifestPath); err == nil {
		var prior upgradeConfigCapsule
		if json.Unmarshal(existing, &prior) != nil || prior.InstanceID != req.InstanceID {
			return capsule, errors.New("invalid existing OpenClaw config upgrade capsule")
		}
		currentSHA := hex.EncodeToString(originalHash[:])
		if currentSHA == prior.TargetSHA256 {
			return prior, nil
		}
		if currentSHA != prior.OriginalSHA256 || capsule.TargetSHA256 != prior.TargetSHA256 {
			return capsule, errors.New("OpenClaw config changed after the upgrade capsule was created")
		}
		capsule = prior
	} else {
		if originalExists {
			backupPath := filepath.Join(dir, "openclaw.json.gz")
			file, createErr := m.io.OpenFile(backupPath+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if createErr != nil {
				return capsule, createErr
			}
			gz := gzip.NewWriter(file)
			_, writeErr := gz.Write(raw)
			closeErr := errors.Join(gz.Close(), file.Sync(), file.Close())
			if err := errors.Join(writeErr, closeErr); err != nil {
				_ = m.io.Remove(backupPath + ".tmp")
				return capsule, err
			}
			if err := m.io.Rename(backupPath+".tmp", backupPath); err != nil {
				return capsule, err
			}
		}
		manifest, _ := json.Marshal(capsule)
		if err := m.io.AtomicWriteFile(manifestPath, append(manifest, '\n'), 0o600); err != nil {
			return capsule, err
		}
	}
	if err := m.io.AtomicWriteFile(configPath, target, 0o600); err != nil {
		return capsule, fmt.Errorf("write normalized OpenClaw config: %w", err)
	}
	if err := m.io.Chown(configPath, req.UID, req.GID); err != nil {
		return capsule, fmt.Errorf("chown normalized OpenClaw config: %w", err)
	}
	return capsule, nil
}

func (m *upgradeService) restoreOpenClawConfig(workspace string, req WorkspaceUpgradeRequest) (bool, error) {
	dir := m.configCapsuleDir(req)
	manifestRaw, err := m.io.ReadFile(filepath.Join(dir, "config-capsule.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var capsule upgradeConfigCapsule
	if err := json.Unmarshal(manifestRaw, &capsule); err != nil || capsule.InstanceID != req.InstanceID {
		return false, errors.New("invalid OpenClaw config rollback capsule")
	}
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if !capsule.OriginalExists {
		if err := m.io.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return true, nil
	}
	file, err := m.io.Open(filepath.Join(dir, "openclaw.json.gz"))
	if err != nil {
		return false, err
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return false, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(gz, maxUpgradeConfigBytes+1))
	closeErr := errors.Join(gz.Close(), file.Close())
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	if len(raw) > maxUpgradeConfigBytes {
		return false, errors.New("OpenClaw config rollback capsule exceeds limit")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != capsule.OriginalSHA256 {
		return false, errors.New("OpenClaw config rollback capsule checksum mismatch")
	}
	mode := os.FileMode(capsule.OriginalMode)
	if mode == 0 {
		mode = 0o600
	}
	if err := m.io.AtomicWriteFile(configPath, raw, mode); err != nil {
		return false, err
	}
	if err := m.io.Chown(configPath, req.UID, req.GID); err != nil {
		return false, err
	}
	return true, nil
}

func (m *upgradeService) prepareOpenClawStateCapsule(workspace string, req WorkspaceUpgradeRequest) (upgradeStateCapsule, error) {
	dir := m.configCapsuleDir(req)
	manifestPath := filepath.Join(dir, "state-capsule.json")
	if raw, err := m.io.ReadFile(manifestPath); err == nil {
		var existing upgradeStateCapsule
		if json.Unmarshal(raw, &existing) != nil || existing.InstanceID != req.InstanceID || existing.SchemaVersion < 1 || existing.SchemaVersion > 3 {
			return upgradeStateCapsule{}, errors.New("invalid existing OpenClaw state upgrade capsule")
		}
		return existing, nil
	}
	if err := m.io.MkdirAll(dir, 0o700); err != nil {
		return upgradeStateCapsule{}, err
	}
	openClawDir := filepath.Join(workspace, "home", ".openclaw")
	capsule := upgradeStateCapsule{SchemaVersion: 2, InstanceID: req.InstanceID, WorkspaceFiles: map[string]bool{}, ControlDirs: map[string]bool{}, ControlFiles: map[string]upgradeStateCapsuleFile{}}
	if m.cfg.OpenClawVersion == "2026.10.2" {
		capsule.SchemaVersion = 3
	}
	entries, readErr := m.io.ReadDir(openClawDir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return upgradeStateCapsule{}, readErr
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "openclaw.json" || upgradeStateCapsuleExcludedRootForVersion(name, capsule.SchemaVersion) {
			continue
		}
		source := filepath.Join(openClawDir, name)
		info, statErr := m.io.Lstat(source)
		if statErr != nil {
			return upgradeStateCapsule{}, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return upgradeStateCapsule{}, fmt.Errorf("state capsule refuses symlink %s", name)
		}
		if entry.IsDir() {
			capsule.ControlDirs[name] = true
			if name == "state" {
				capsule.OriginalExists = true
			}
			var bytes, files int64
			var err error
			if capsule.SchemaVersion >= 3 && upgradePluginControlRoot(name) {
				bytes, files, err = m.copyUpgradePluginTree(source, filepath.Join(dir, "control-original", name), openClawDir)
			} else {
				bytes, files, err = m.copyTreeStable(source, filepath.Join(dir, "control-original", name))
			}
			if err != nil {
				return upgradeStateCapsule{}, fmt.Errorf("capture OpenClaw %s capsule: %w", name, err)
			}
			capsule.TotalBytes += bytes
			capsule.FileCount += files
			continue
		}
		if !info.Mode().IsRegular() {
			return upgradeStateCapsule{}, fmt.Errorf("state capsule refuses non-regular file %s", name)
		}
		if err := m.captureUpgradeStateFile(source, name, dir, &capsule); err != nil {
			return upgradeStateCapsule{}, err
		}
	}
	// Agent-scoped session transcripts use OpenClaw's official session archive,
	// but Doctor can retire files in each agent's control directory.  Preserve
	// the control directory without duplicating the potentially large sessions.
	agentControls, err := m.io.Glob(filepath.Join(openClawDir, "agents", "*", "agent"))
	if err != nil {
		return upgradeStateCapsule{}, err
	}
	for _, source := range agentControls {
		info, statErr := m.io.Lstat(source)
		if statErr != nil || !info.IsDir() {
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return upgradeStateCapsule{}, statErr
			}
			continue
		}
		rel, relErr := filepath.Rel(openClawDir, source)
		if relErr != nil || !safeCapsuleRelativePath(rel) {
			return upgradeStateCapsule{}, ErrWorkspacePath
		}
		capsule.ControlDirs[filepath.ToSlash(rel)] = true
		bytes, files, copyErr := m.copyTreeStable(source, filepath.Join(dir, "control-original", rel))
		if copyErr != nil {
			return upgradeStateCapsule{}, fmt.Errorf("capture OpenClaw %s capsule: %w", rel, copyErr)
		}
		capsule.TotalBytes += bytes
		capsule.FileCount += files
	}
	workspaceDir := filepath.Join(workspace, "home", ".openclaw", "workspace")
	workspaceCapsule := filepath.Join(dir, "workspace-original")
	if err := m.io.MkdirAll(workspaceCapsule, 0o700); err != nil {
		return upgradeStateCapsule{}, err
	}
	for _, name := range []string{"HEARTBEAT.md", "TOOLS.md", "openclaw-workspace-state.json", filepath.Join(".openclaw", "workspace-state.json")} {
		source := filepath.Join(workspaceDir, name)
		fileInfo, err := m.io.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			capsule.WorkspaceFiles[name] = false
			continue
		}
		if err != nil {
			return upgradeStateCapsule{}, err
		}
		if !fileInfo.Mode().IsRegular() {
			return upgradeStateCapsule{}, fmt.Errorf("OpenClaw workspace migration input %s is not a regular file", name)
		}
		capsule.WorkspaceFiles[name] = true
		workspaceTarget := filepath.Join(workspaceCapsule, name)
		if err := m.io.MkdirAll(filepath.Dir(workspaceTarget), 0o700); err != nil {
			return upgradeStateCapsule{}, err
		}
		if err := m.copyRegularFileStable(source, workspaceTarget, fileInfo.Mode().Perm()); err != nil {
			return upgradeStateCapsule{}, err
		}
		capsule.TotalBytes += fileInfo.Size()
		capsule.FileCount++
	}
	raw, _ := json.Marshal(capsule)
	if err := m.io.AtomicWriteFile(manifestPath, append(raw, '\n'), 0o600); err != nil {
		return upgradeStateCapsule{}, err
	}
	return capsule, nil
}

func upgradeStateCapsuleExcludedRoot(root string) bool {
	normalized := strings.ToLower(strings.TrimSpace(root))
	if strings.HasPrefix(normalized, "session-sqlite") || strings.HasPrefix(normalized, "sessions.sqlite") {
		return true
	}
	switch normalized {
	case "workspace", "agents", "tmp", "cache", "xdg-state", "browser", "canvas", "media", "sandboxes", "extensions", "npm", "plugin-skills":
		return true
	default:
		return false
	}
}

func safeCapsuleRelativePath(rel string) bool {
	clean := filepath.Clean(rel)
	return clean != "." && clean != "" && clean != ".." && !filepath.IsAbs(clean) && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func (m *upgradeService) captureUpgradeStateFile(source, rel, capsuleDir string, capsule *upgradeStateCapsule) error {
	if capsule == nil || !safeCapsuleRelativePath(rel) {
		return ErrWorkspacePath
	}
	info, err := m.io.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state capsule input is not a regular file: %s", rel)
	}
	target := filepath.Join(capsuleDir, "control-original", filepath.FromSlash(rel))
	if err := m.io.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := m.copyRegularFileStable(source, target, info.Mode().Perm()); err != nil {
		return err
	}
	raw, err := m.io.ReadFile(target)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	capsule.ControlFiles[filepath.ToSlash(rel)] = upgradeStateCapsuleFile{Mode: uint32(info.Mode().Perm()), Size: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}
	capsule.TotalBytes += int64(len(raw))
	capsule.FileCount++
	return nil
}

func (m *upgradeService) copyTreeStable(source, target string) (int64, int64, error) {
	var totalBytes, files int64
	err := m.io.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrWorkspacePath
		}
		destination := filepath.Join(target, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("state capsule refuses symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return m.io.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("state capsule refuses non-regular file %s", rel)
		}
		if err := m.copyRegularFileStable(path, destination, info.Mode().Perm()); err != nil {
			return err
		}
		totalBytes += info.Size()
		files++
		return nil
	})
	return totalBytes, files, err
}

func (m *upgradeService) restoreOpenClawStateCapsule(workspace string, req WorkspaceUpgradeRequest) (bool, error) {
	dir := m.configCapsuleDir(req)
	raw, err := m.io.ReadFile(filepath.Join(dir, "state-capsule.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var capsule upgradeStateCapsule
	if json.Unmarshal(raw, &capsule) != nil || capsule.InstanceID != req.InstanceID || capsule.SchemaVersion < 1 || capsule.SchemaVersion > 3 {
		return false, errors.New("invalid OpenClaw state rollback capsule")
	}
	openClawDir := filepath.Join(workspace, "home", ".openclaw")
	if capsule.SchemaVersion >= 2 {
		entries, readErr := m.io.ReadDir(openClawDir)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return false, readErr
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() && !upgradeStateCapsuleExcludedRootForVersion(name, capsule.SchemaVersion) && !capsule.ControlDirs[name] {
				if err := m.quarantineUpgradeTarget(filepath.Join(openClawDir, name), filepath.Join(dir, "control-failed-target", name)); err != nil {
					return false, err
				}
			}
			if !entry.IsDir() && name != "openclaw.json" && !upgradeStateCapsuleExcludedRootForVersion(name, capsule.SchemaVersion) {
				if _, existed := capsule.ControlFiles[name]; !existed {
					if err := m.quarantineUpgradeTarget(filepath.Join(openClawDir, name), filepath.Join(dir, "control-failed-target-files", name)); err != nil {
						return false, err
					}
				}
			}
		}
	}
	directories := make([]string, 0, len(capsule.ControlDirs))
	for name, existed := range capsule.ControlDirs {
		if existed {
			directories = append(directories, name)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], "/") > strings.Count(directories[j], "/")
	})
	for _, name := range directories {
		rel := filepath.FromSlash(name)
		if !safeCapsuleRelativePath(rel) {
			return false, errors.New("invalid OpenClaw state rollback path")
		}
		target := filepath.Join(openClawDir, rel)
		failed := filepath.Join(dir, "control-failed-target", rel)
		if _, err := m.io.Stat(failed); errors.Is(err, os.ErrNotExist) {
			if _, currentErr := m.io.Stat(target); currentErr == nil {
				if err := m.io.MkdirAll(filepath.Dir(failed), 0o700); err != nil {
					return false, err
				}
				if err := m.io.Rename(target, failed); err != nil {
					return false, fmt.Errorf("quarantine target OpenClaw %s: %w", name, err)
				}
			} else if !errors.Is(currentErr, os.ErrNotExist) {
				return false, currentErr
			}
		}
		original := filepath.Join(dir, "control-original", rel)
		if _, err := m.io.Stat(target); errors.Is(err, os.ErrNotExist) {
			if err := m.io.Rename(original, target); err != nil {
				return false, fmt.Errorf("restore original OpenClaw %s: %w", name, err)
			}
		} else if err != nil {
			return false, err
		}
		if err := m.chownTree(target, req.UID, req.GID); err != nil {
			return false, err
		}
	}
	files := make([]string, 0, len(capsule.ControlFiles))
	for rel := range capsule.ControlFiles {
		files = append(files, rel)
	}
	sort.Strings(files)
	for _, slash := range files {
		rel := filepath.FromSlash(slash)
		if !safeCapsuleRelativePath(rel) {
			return false, errors.New("invalid OpenClaw state rollback file")
		}
		meta := capsule.ControlFiles[slash]
		target := filepath.Join(openClawDir, rel)
		failed := filepath.Join(dir, "control-failed-target-files", rel)
		if err := m.quarantineUpgradeTarget(target, failed); err != nil {
			return false, err
		}
		original := filepath.Join(dir, "control-original", rel)
		data, readErr := m.io.ReadFile(original)
		if readErr != nil {
			return false, readErr
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != meta.Size || hex.EncodeToString(digest[:]) != meta.SHA256 {
			return false, fmt.Errorf("OpenClaw rollback capsule checksum mismatch: %s", slash)
		}
		mode := os.FileMode(meta.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := m.io.AtomicWriteFile(target, data, mode); err != nil {
			return false, err
		}
		if err := m.io.Chown(target, req.UID, req.GID); err != nil {
			return false, err
		}
	}
	workspaceDir := filepath.Join(workspace, "home", ".openclaw", "workspace")
	failedWorkspace := filepath.Join(dir, "workspace-failed-target")
	workspaceFiles := []string{"HEARTBEAT.md", "TOOLS.md"}
	// Schema v1 capsules predate the workspace-state inputs.  Treating an
	// absent v1 manifest entry as "did not exist" would incorrectly quarantine
	// a user's pre-existing file during a rollback of an older rollout.
	if capsule.SchemaVersion >= 2 {
		workspaceFiles = append(workspaceFiles, "openclaw-workspace-state.json", filepath.Join(".openclaw", "workspace-state.json"))
	}
	for _, name := range workspaceFiles {
		target := filepath.Join(workspaceDir, name)
		if capsule.WorkspaceFiles[name] {
			source := filepath.Join(dir, "workspace-original", name)
			info, err := m.io.Stat(source)
			if err != nil {
				return false, err
			}
			data, err := m.io.ReadFile(source)
			if err != nil {
				return false, err
			}
			if err := m.io.AtomicWriteFile(target, data, info.Mode().Perm()); err != nil {
				return false, err
			}
			if err := m.io.Chown(target, req.UID, req.GID); err != nil {
				return false, err
			}
			continue
		}
		if _, err := m.io.Stat(target); err == nil {
			if err := m.io.MkdirAll(failedWorkspace, 0o700); err != nil {
				return false, err
			}
			if err := m.io.Rename(target, filepath.Join(failedWorkspace, name)); err != nil {
				return false, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

func (m *upgradeService) quarantineUpgradeTarget(target, failed string) error {
	if _, err := m.io.Stat(failed); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := m.io.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := m.io.MkdirAll(filepath.Dir(failed), 0o700); err != nil {
		return err
	}
	if err := m.io.Rename(target, failed); err != nil {
		return fmt.Errorf("quarantine failed OpenClaw target %s: %w", target, err)
	}
	return nil
}

func (m *upgradeService) sessionMigrationArchiveStats(home string) (int64, int64, error) {
	var bytes, files int64
	err := m.io.WalkDir(filepath.Join(home, ".openclaw"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(filepath.Join(home, ".openclaw"), path)
		if relErr != nil {
			return relErr
		}
		slash := filepath.ToSlash(rel)
		if !strings.Contains(slash, "session-sqlite-import-archive/") && !strings.Contains(slash, "session-sqlite-migration-runs/") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files++
		bytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	return bytes, files, err
}

func sessionSQLiteMigrationCommands() [][]string {
	return [][]string{
		{"doctor", "--session-sqlite", "dry-run", "--session-sqlite-all-agents", "--json", "--non-interactive"},
		{"doctor", "--session-sqlite", "import", "--session-sqlite-all-agents", "--json", "--non-interactive", "--yes"},
		{"doctor", "--session-sqlite", "validate", "--session-sqlite-all-agents", "--json", "--non-interactive"},
	}
}

func (m *upgradeService) PreflightWorkspace(ctx context.Context, req WorkspaceUpgradeRequest) (WorkspaceInventory, error) {
	ctx = withReadOnlyUpgradeCommands(ctx)
	closeIO, ioErr := m.beginIO(req, req.OfficialDBCheck)
	if ioErr != nil {
		return WorkspaceInventory{}, ioErr
	}
	defer closeIO()

	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return WorkspaceInventory{}, err
	}
	if m.host.InstanceActive(req.InstanceID) {
		return WorkspaceInventory{}, fmt.Errorf("instance %d still has an active gateway", req.InstanceID)
	}
	realRoot, err := m.io.EvalSymlinks(m.cfg.WorkspaceRoot)
	if err != nil {
		return WorkspaceInventory{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	realWorkspace, err := m.io.EvalSymlinks(workspace)
	if err != nil {
		return WorkspaceInventory{}, fmt.Errorf("resolve instance workspace: %w", err)
	}
	if !pathWithin(realRoot, realWorkspace) {
		return WorkspaceInventory{}, ErrWorkspacePath
	}
	result := WorkspaceInventory{WorkspacePath: workspace, OpenClawHome: filepath.Join(workspace, "home", ".openclaw"), CheckedAt: time.Now().UTC()}
	result.AvailableBytes, _ = availableFilesystemBytes(workspace)
	// Inventory only OpenClaw's session migration inputs. Walking the complete
	// workspace made unrelated project *.db files part of the upgrade contract
	// and incorrectly required space for a full-workspace duplicate.
	err = m.io.WalkDir(result.OpenClawHome, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(result.OpenClawHome, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrWorkspacePath
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() && !sessionMigrationDirectory(filepath.ToSlash(rel)) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && !sessionMigrationFile(filepath.ToSlash(rel)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result.DirectoryCount++
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.SymlinkCount++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		result.FileCount++
		result.TotalBytes += info.Size()
		if isOpenClawSessionDatabase(filepath.ToSlash(rel), entry.Name()) {
			fact, err := m.preflightDatabase(ctx, workspace, path, info.Size(), req.OfficialDBCheck)
			if err != nil {
				return err
			}
			result.DatabaseFiles = append(result.DatabaseFiles, fact)
		}
		return nil
	})
	if err != nil {
		return WorkspaceInventory{}, err
	}
	sort.Slice(result.DatabaseFiles, func(i, j int) bool {
		return result.DatabaseFiles[i].RelativePath < result.DatabaseFiles[j].RelativePath
	})
	const migrationReserve = uint64(64 << 20)
	need := uint64(result.TotalBytes) + migrationReserve
	if result.AvailableBytes > 0 && need > result.AvailableBytes {
		return WorkspaceInventory{}, fmt.Errorf("insufficient session migration capacity: need at least %d bytes, available %d", need, result.AvailableBytes)
	}
	return result, nil
}

func sessionMigrationDirectory(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return true
	}
	parts := strings.Split(rel, "/")
	if parts[0] == "sessions" || parts[0] == "session-sqlite-import-archive" || parts[0] == "session-sqlite-migration-runs" {
		return true
	}
	if parts[0] != "agents" {
		return false
	}
	if len(parts) <= 2 {
		return true
	}
	return parts[2] == "sessions" || parts[2] == "session-sqlite-import-archive" || parts[2] == "session-sqlite-migration-runs"
}

func sessionMigrationFile(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	if base == "sessions.json" || base == "sessions.sqlite" || base == "sessions.sqlite3" || base == "sessions.db" {
		return true
	}
	if strings.Contains(lower, "session-sqlite-import-archive/") || strings.Contains(lower, "session-sqlite-migration-runs/") {
		return true
	}
	return strings.Contains(lower, "/sessions/") && (strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".json"))
}

func isOpenClawSessionDatabase(rel, name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	if base != "sessions.sqlite" && base != "sessions.sqlite3" && base != "sessions.db" {
		return false
	}
	return sessionMigrationFile(rel) && isSQLiteMainDatabase(base)
}

func isSQLiteMainDatabase(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".sqlite-wal", ".sqlite-shm", ".sqlite-journal", ".sqlite3-wal", ".sqlite3-shm", ".sqlite3-journal", ".db-wal", ".db-shm", ".db-journal"} {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	for _, suffix := range []string{".lock.sqlite", ".lock.sqlite3", ".lock.db"} {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	return strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".sqlite3") || strings.HasSuffix(lower, ".db")
}

func (m *upgradeService) preflightDatabase(ctx context.Context, workspace, source string, size int64, official bool) (DatabasePreflight, error) {
	rel, _ := filepath.Rel(workspace, source)
	fact := DatabasePreflight{RelativePath: filepath.ToSlash(rel), SizeBytes: size}
	file, err := m.io.Open(source)
	if err != nil {
		return fact, err
	}
	header := make([]byte, 16)
	n, readErr := io.ReadFull(file, header)
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return fact, readErr
	}
	fact.SQLiteHeader = n == 16 && string(header) == "SQLite format 3\x00"
	if !fact.SQLiteHeader {
		return fact, fmt.Errorf("database %s does not have a valid SQLite header", fact.RelativePath)
	}
	if !official {
		return fact, nil
	}
	tmpDir, err := m.io.MkdirTemp(m.io.controlRoot(), "db-preflight-")
	if err != nil {
		return fact, err
	}
	defer m.io.RemoveAll(tmpDir)
	copyPath := filepath.Join(tmpDir, "database.sqlite")
	if err := m.io.CopyRegularFile(source, copyPath, 0o600); err != nil {
		return fact, err
	}
	req := m.io.operation
	if m.cfg.IsolatedLifecycle {
		if err := m.io.Chown(tmpDir, req.UID, req.GID); err != nil {
			return fact, err
		}
		if err := m.io.Chown(copyPath, req.UID, req.GID); err != nil {
			return fact, err
		}
	}
	phaseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(phaseCtx, "openclaw", "database", "preflight", copyPath, "--json")
	command.Dir = tmpDir
	command.Env = m.upgradeCommandEnv(map[string]string{"HOME": filepath.Join(workspace, "home"), "NO_COLOR": "1"})
	configureGatewayCommand(command, req.UID, req.GID)
	if !m.cfg.IsolatedLifecycle {
		inheritUpgradeOperationFence(ctx, command)
		cleanup := fenceUpgradeCommand(command)
		defer cleanup()
	}
	output, err := m.combinedUpgradeOutput(phaseCtx, command, req)
	if len(output) > 64*1024 {
		output = output[:64*1024]
	}
	digest := sha256.Sum256(output)
	fact.OutputSHA256 = hex.EncodeToString(digest[:])
	if err != nil {
		return fact, fmt.Errorf("OpenClaw database preflight failed for %s: %w", fact.RelativePath, err)
	}
	fact.OfficialOK = true
	return fact, nil
}
