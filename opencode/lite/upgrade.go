package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

const (
	OpenCodeUpgradeProtocol = "opencode-preserve-v1"
	OpenCodeNativeVersion   = "1.18.13"
	// Official schema reference, separately verified against an empty DB made
	// by the observed binary. Binary identity is pinned independently below.
	OpenCodeNativeRevision     = "a105350812f05f914c768e468559dbd6bd508d8e"
	OpenCodeNativeBinarySHA256 = "bcebc898d22419417b8dbec0f5e3f00f5b67b5a38c1de37c77c699d0e31fe0dd"
)

var OpenCodeUpgradeCapabilities = []string{
	"opencode.native-preserve-v1", "opencode.workspace.writer-lease",
	"opencode.session-continuity-v1", "opencode.runtime-standby-v1",
	"opencode.native-payload-poduid-v1",
}

// These reviewed upstream inputs are tied to their immutable revision and
// SHA256 provenance in upgrade_schema.json. They never come from a user DB.
//
//go:embed upgrade_schema.json
var upgradeSchema json.RawMessage

//go:embed upgrade_helper.py
var upgradePython string

type upgradeService struct {
	cfg    gateway.Config
	host   gateway.UpgradeHost
	scoped bool
}

func (Profile) UpgradeService(cfg gateway.Config, host gateway.UpgradeHost) gateway.RuntimeUpgradeService {
	return &upgradeService{cfg: cfg, host: host}
}

// All production I/O happens inside the existing offline maintenance container.
// Direct legacy HTTP calls cannot bypass its writer and teardown proofs.
func (Profile) RunScopedNativeUpgrade(ctx context.Context, cfg gateway.Config, op gateway.NativeUpgradeOperation, host gateway.UpgradeHost) (json.RawMessage, error) {
	if err := gateway.ValidateUpgradePodIdentity(cfg, op.Request); err != nil {
		return nil, err
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	if cfg.RuntimeType != "opencode" || cfg.SandboxProfile != "nfs-rootless-v1" || cfg.NetworkSecurityProfile != "isolated-network-v1" || op.Request.LeaseToken != "" {
		return nil, errors.New("OpenCode native preservation requires scoped maintenance")
	}
	if err := workspaceio.ValidateInstanceCaller(op.Request.UID, op.Request.GID); err != nil {
		return nil, err
	}
	service := &upgradeService{cfg: cfg, host: host, scoped: true}
	return service.run(ctx, op.Action, op.Request)
}

func unsupportedOpenCodeNativeEnvironment() error {
	for _, key := range []string{"OPENCODE_DB", "OPENCODE_CHANNEL", "OPENCODE_DISABLE_CHANNEL_DB"} {
		if _, present := os.LookupEnv(key); present {
			return errors.New("OpenCode native database/channel overrides are unsupported")
		}
	}
	return nil
}

func validateOpenCodeNativePayloadFiles(ctx context.Context, paths nativePayloadPaths) error {
	if err := unsupportedOpenCodeNativeEnvironment(); err != nil {
		return err
	}
	if _, err := readNativePayloadKey(paths); err != nil {
		return err
	}
	for _, path := range []string{paths.python, paths.binary, paths.attestation} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("OpenCode native preservation payload is unavailable")
		}
	}
	raw, err := os.ReadFile(paths.attestation)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("OpenCode native revision is unavailable")
	}
	var manifest struct {
		Version      string `json:"native_version"`
		Revision     string `json:"official_source_reference"`
		BinarySHA256 string `json:"observed_binary_sha256"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Revision != OpenCodeNativeRevision || manifest.Version != OpenCodeNativeVersion || manifest.BinarySHA256 != OpenCodeNativeBinarySHA256 {
		return errors.New("OpenCode native revision is unsupported")
	}
	binary, err := os.Open(paths.binary)
	if err != nil {
		return errors.New("OpenCode native binary is unavailable")
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, binary)
	closeErr := binary.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if hashErr != nil || closeErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != OpenCodeNativeBinarySHA256 {
		return errors.New("OpenCode native binary differs from the preserved source image")
	}
	cmd := exec.CommandContext(ctx, paths.binary, "--version")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/tmp"}
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != OpenCodeNativeVersion {
		return errors.New("OpenCode native version is unsupported")
	}
	return nil
}

func (s *upgradeService) run(ctx context.Context, action string, req gateway.WorkspaceUpgradeRequest) (json.RawMessage, error) {
	if !s.scoped || s.host == nil {
		return nil, errors.New("OpenCode preservation requires the native maintenance sandbox")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.ResumeRolloutID != "" || req.RecoveryMode != "" {
		return nil, errors.New("OpenCode cross-operation recovery is unsupported; resume the same operation")
	}
	if req.ResumeGeneration != 0 && (action != "restore" && action != "restore-status" || req.ResumeGeneration < 1 || (req.Generation != req.ResumeGeneration && req.Generation != req.ResumeGeneration+1)) {
		return nil, errors.New("OpenCode restore generation does not match its original capsule")
	}
	if err := validateOpenCodeNativePayload(ctx); err != nil {
		return nil, err
	}
	workspace, err := s.host.WorkspaceForUpgrade(req)
	if err != nil {
		return nil, err
	}
	if err = gateway.ValidateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return nil, err
	}
	capsule := filepath.Join(s.host.UpgradeRoot(), "rollout-"+req.RolloutID, fmt.Sprintf("instance-%d", req.InstanceID))
	for _, path := range []string{workspace, capsule} {
		root, openErr := workspaceio.OpenAt(s.cfg.WorkspaceRoot, path)
		if openErr != nil {
			return nil, errors.New("OpenCode preservation scope is unavailable")
		}
		root.Close()
	}
	mutates := action == "migrate" || action == "restore"
	if mutates {
		if s.host.InstanceActive(req.InstanceID) {
			return nil, errors.New("OpenCode source writer remains active")
		}
		if err = s.host.VerifyUpgradeWriter(ctx, req); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(map[string]any{"action": action, "workspace": workspace, "capsule": capsule, "request": req, "schema": upgradeSchema})
	if err != nil {
		return nil, err
	}
	// -I ignores PYTHONPATH, the working directory and user site packages. The
	// helper is embedded trusted code; no executable is loaded from a workspace.
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-B", "-c", upgradePython)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/tmp"}
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, preservationProcessError(ctx, err, stderr.String(), mutates)
	}
	if len(output) == 0 || len(output) > 1<<20 || !json.Valid(output) {
		return nil, gateway.ErrNativeStorageUncertain
	}
	if mutates {
		if err = s.host.VerifyUpgradeWriter(ctx, req); err != nil {
			return nil, errors.Join(gateway.ErrNativeStorageUncertain, err)
		}
	}
	return output, nil
}

func preservationProcessError(ctx context.Context, err error, stderr string, mutates bool) error {
	if ctx.Err() != nil || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) {
		return errors.Join(gateway.ErrNativeStorageUncertain, err)
	}
	code := safePreservationCode(stderr)
	var exited *exec.ExitError
	if errors.As(err, &exited) && (exited.ExitCode() == 75 || mutates && (exited.ExitCode() != 1 || !strings.HasPrefix(code, "native_rejected_"))) {
		return errors.Join(gateway.ErrNativeStorageUncertain, err)
	}
	if mutates && !errors.As(err, &exited) {
		return errors.Join(gateway.ErrNativeStorageUncertain, err)
	}
	// The helper emits fixed codes only. Do not expose Python diagnostics,
	// SQLite values, native output, or user-controlled paths.
	return errors.New("OpenCode native preservation failed: " + code)
}

func safePreservationCode(raw string) string {
	code := strings.TrimSpace(raw)
	if len(code) == 0 || len(code) > 80 {
		return "helper_failed"
	}
	for _, r := range code {
		if r != '_' && (r < 'a' || r > 'z') {
			return "helper_failed"
		}
	}
	return code
}

func (s *upgradeService) PreflightUpgradeCompatibility(ctx context.Context, req gateway.WorkspaceUpgradeRequest) (result gateway.UpgradeCompatibilityResult, err error) {
	raw, err := s.run(ctx, "compatibility", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s *upgradeService) PreflightWorkspace(ctx context.Context, req gateway.WorkspaceUpgradeRequest) (result gateway.WorkspaceInventory, err error) {
	raw, err := s.run(ctx, "inventory", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s *upgradeService) MigrateSessionSQLite(ctx context.Context, req gateway.WorkspaceUpgradeRequest) (result gateway.SessionSQLiteMigrationResult, err error) {
	raw, err := s.run(ctx, "migrate", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s *upgradeService) SessionSQLiteMigrationStatus(req gateway.WorkspaceUpgradeRequest) (result gateway.SessionSQLiteMigrationResult, err error) {
	raw, err := s.run(context.Background(), "migration-status", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s *upgradeService) RestoreSessionSQLite(ctx context.Context, req gateway.WorkspaceUpgradeRequest) (result gateway.SessionSQLiteRestoreResult, err error) {
	raw, err := s.run(ctx, "restore", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
func (s *upgradeService) SessionSQLiteRestoreStatus(req gateway.WorkspaceUpgradeRequest) (result gateway.SessionSQLiteRestoreResult, err error) {
	raw, err := s.run(context.Background(), "restore-status", req)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
