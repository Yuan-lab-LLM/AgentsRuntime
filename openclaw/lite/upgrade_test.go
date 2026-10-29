package openclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSnapshotVerifyAndAtomicRestore(t *testing.T) {
	root := t.TempDir()
	cfg := Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target"}
	mgr := NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-1", SnapshotID: "source-7-1", InstanceID: 12, UserID: 34, Generation: 2, LeaseToken: "lease-token-1"}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	openclawHome := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(openclawHome, 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(openclawHome, "openclaw.json")
	if err := os.WriteFile(configPath, []byte("source-7.1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(openclawHome, "sessions.sqlite")
	if err := os.WriteFile(dbPath, append([]byte("SQLite format 3\x00"), make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, 10*time.Minute); err != nil {
		t.Fatalf("same rollout could not idempotently renew writer lease: %v", err)
	}
	inventory, err := mgr.PreflightWorkspace(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.FileCount != 1 || len(inventory.DatabaseFiles) != 1 || !inventory.DatabaseFiles[0].SQLiteHeader {
		t.Fatalf("inventory = %+v", inventory)
	}
	_, err = mgr.CreateWorkspaceSnapshot(context.Background(), req)
	if err == nil {
		t.Fatal("full workspace snapshot unexpectedly remained enabled")
	}
}

func TestWorkspacePreflightIgnoresSQLiteLockAndCompanionFiles(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-preflight", SnapshotID: "instance-75-generation-1", InstanceID: 75, UserID: 1, Generation: 1, LeaseToken: "lease-preflight"}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	openclawHome := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(filepath.Join(openclawHome, "tmp", "openclaw-200075"), 0o700); err != nil {
		t.Fatal(err)
	}
	validSQLite := append([]byte("SQLite format 3\x00"), make([]byte, 64)...)
	files := map[string][]byte{
		filepath.Join(openclawHome, "sessions.sqlite"):                                                validSQLite,
		filepath.Join(openclawHome, "tmp", "openclaw-200075", "device-identity.98ce393a.lock.sqlite"): nil,
		filepath.Join(openclawHome, "sessions.sqlite-wal"):                                            []byte("wal bytes"),
		filepath.Join(openclawHome, "sessions.sqlite-shm"):                                            []byte("shm bytes"),
		filepath.Join(openclawHome, "sessions.sqlite-journal"):                                        []byte("journal bytes"),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := mgr.PreflightWorkspace(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.FileCount != 1 || len(inventory.DatabaseFiles) != 1 || inventory.DatabaseFiles[0].RelativePath != "home/.openclaw/sessions.sqlite" {
		t.Fatalf("inventory = %+v", inventory)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err = mgr.CreateWorkspaceSnapshot(context.Background(), req)
	if err == nil {
		t.Fatal("full workspace snapshot unexpectedly remained enabled")
	}
}

func TestWorkspacePreflightRejectsCorruptSQLiteMainDatabase(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-corrupt", InstanceID: 76, UserID: 1, Generation: 1}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(workspace, "home", ".openclaw", "sessions.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("not a sqlite database")
	if err := os.WriteFile(dbPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.PreflightWorkspace(context.Background(), req); err == nil {
		t.Fatal("corrupt SQLite main database was accepted")
	}
	got, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("preflight changed corrupt database bytes")
	}
}

func TestWorkspacePreflightIgnoresUnrelatedProjectDatabase(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-project-db", InstanceID: 77, UserID: 1, Generation: 1}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(workspace, "project", "application.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("application-owned bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := mgr.PreflightWorkspace(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.FileCount != 0 || len(inventory.DatabaseFiles) != 0 {
		t.Fatalf("unrelated project database entered session migration inventory: %+v", inventory)
	}
}

func TestSessionSQLiteMigrationUsesOfficialTransactionalPhases(t *testing.T) {
	commands := sessionSQLiteMigrationCommands()
	if len(commands) != 3 {
		t.Fatalf("command count = %d", len(commands))
	}
	for index, phase := range []string{"dry-run", "import", "validate"} {
		if len(commands[index]) < 6 || commands[index][0] != "doctor" || commands[index][1] != "--session-sqlite" || commands[index][2] != phase {
			t.Fatalf("command %d = %#v", index, commands[index])
		}
	}
	if commands[1][len(commands[1])-1] != "--yes" {
		t.Fatalf("import command must be non-interactive: %#v", commands[1])
	}
}

func TestDoctorMigrationAuthorityIsRestrictedToNonInteractiveRepair(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"doctor", "--fix", "--yes", "--non-interactive"}, want: true},
		{args: []string{"doctor", "--repair", "--non-interactive"}, want: true},
		{args: []string{"doctor", "--fix"}, want: false},
		{args: []string{"config", "validate", "--non-interactive", "--fix"}, want: false},
		{args: []string{"gateway", "run"}, want: false},
	} {
		if got := isOpenClawDoctorRepairCommand(test.args); got != test.want {
			t.Fatalf("isOpenClawDoctorRepairCommand(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestDoctorFailureSummaryReportsLegacyExecApprovals(t *testing.T) {
	got := safeOpenClawFailureSummary([]byte("Legacy exec approvals exist at /secret/home/.openclaw/exec-approvals.json. Run openclaw doctor --fix.\n"))
	if !strings.Contains(strings.ToLower(got), "legacy exec approvals") || strings.Contains(got, "/secret/home") {
		t.Fatalf("summary = %q", got)
	}
}

func TestSessionSQLiteRestoreReceiptModeMustMatchRequest(t *testing.T) {
	result := SessionSQLiteRestoreResult{InstanceID: 209, Status: "restored", PreservedSessionSQLite: true}
	request := WorkspaceUpgradeRequest{InstanceID: 209, PreserveSessionSQLite: true}
	if !sessionRestoreReceiptMatches(result, request) {
		t.Fatal("matching SQLite-preserving receipt was rejected")
	}
	request.PreserveSessionSQLite = false
	if sessionRestoreReceiptMatches(result, request) {
		t.Fatal("SQLite-preserving receipt was reused for a legacy archive restore")
	}
}

func TestRestoreSessionSQLitePreservesExistingDatabaseFor81Source(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", WorkspaceRoot: root, PodUID: "pod-target", OpenClawVersion: OpenClaw81Version}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-preserve", InstanceID: 209, UserID: 1, Generation: 3, LeaseToken: "lease-preserve", PreserveSessionSQLite: true, UID: os.Geteuid(), GID: os.Getegid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	openClawDir := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(openClawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(openClawDir, "openclaw.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(openClawDir, "sessions.sqlite")
	sqliteBytes := []byte("8.1-sqlite-sentinel")
	if err := os.WriteFile(sqlitePath, sqliteBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.prepareOpenClaw81Config(workspace, req); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.prepareOpenClawStateCapsule(workspace, req); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(openClawDir, "session-sqlite-import-archive", "legacy.jsonl")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, time.Minute); err != nil {
		t.Fatal(err)
	}
	result, err := mgr.RestoreSessionSQLite(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PreservedSessionSQLite || !result.ConfigRestored || !result.StateRestored {
		t.Fatalf("restore result = %+v", result)
	}
	got, err := os.ReadFile(sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sqliteBytes) {
		t.Fatalf("8.1 SQLite changed during rollback: %q", got)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("legacy archive changed during 8.1 rollback: %v", err)
	}
}

func TestCanonicalSessionCatalog(t *testing.T) {
	raw := []byte(`{"count":2,"sessions":[{"key":"agent:main:second","sessionId":"b"},{"key":"agent:main:main","sessionId":"a"}]}`)
	count, digest, err := canonicalSessionCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("session count = %d, want 2", count)
	}
	want := sha256.Sum256([]byte("agent:main:main\x00a\nagent:main:second\x00b\n"))
	if digest != hex.EncodeToString(want[:]) {
		t.Fatalf("catalog digest = %s, want %x", digest, want)
	}
}

func TestCanonicalSessionCatalogAcceptsSurroundingDiagnostics(t *testing.T) {
	raw := []byte("Config warning: deprecated field\n" +
		`{"count":1,"sessions":[{"key":"agent:main:main","sessionId":"a"}]}` +
		"\nCompleted with warnings\n")
	count, digest, err := canonicalSessionCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || digest == "" {
		t.Fatalf("catalog = %d, %q", count, digest)
	}
}

func TestCanonicalSessionCatalogRejectsAmbiguousCatalogs(t *testing.T) {
	raw := []byte(`{"sessions":[]}` + "\n" + `{"sessions":[]}`)
	if _, _, err := canonicalSessionCatalog(raw); err == nil {
		t.Fatal("ambiguous catalogs were accepted")
	}
}

func TestCanonicalSessionCatalogRejectsDiagnosticOnlyOutput(t *testing.T) {
	if _, _, err := canonicalSessionCatalog([]byte("Config warning only")); err == nil {
		t.Fatal("diagnostic-only output was accepted")
	}
}

func TestCanonicalSessionCatalogRejectsIncompleteEntry(t *testing.T) {
	if _, _, err := canonicalSessionCatalog([]byte(`{"sessions":[{"key":"agent:main:main"}]}`)); err == nil {
		t.Fatal("incomplete session catalog was accepted")
	}
}

func TestSessionSQLiteMigrationFailsClosedWithoutOwner(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-migrate", InstanceID: 23, UserID: 8, Generation: 1, LeaseToken: "lease-migrate"}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "home", ".openclaw"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireWriterLease(req, req.LeaseToken, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.MigrateSessionSQLite(context.Background(), req); err == nil {
		t.Fatal("migration succeeded without an explicit uid/gid")
	}
}

func TestSessionSQLiteReceiptStatusIsReadOnlyAndIdentityBound(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-status", InstanceID: 29, UserID: 8, Generation: 1}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	want := SessionSQLiteMigrationResult{InstanceID: req.InstanceID, Status: "validated", OutputSHA256: "output", SessionCatalogSHA256: "catalog", SessionCount: 1}
	if err := writeSessionMigrationReceipt(mgr.sessionMigrationReceiptPath(req), want); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.SessionSQLiteMigrationStatus(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != want.Status || got.OutputSHA256 != want.OutputSHA256 {
		t.Fatalf("receipt = %+v, want %+v", got, want)
	}
	missing := req
	missing.InstanceID++
	if _, err := mgr.SessionSQLiteMigrationStatus(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt error = %v, want os.ErrNotExist", err)
	}
}

func TestUpgradeConfigCapsuleNormalizesAndRestoresLegacyConfig(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-config", InstanceID: 24, UserID: 8, Generation: 1, UID: os.Geteuid(), GID: os.Getegid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workspace, "home", ".openclaw", "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"cron":{"runLog":true,"maxConcurrentRuns":2},"gateway":{"nodes":{"denyCommands":["screen.record"]}},"agents":{"defaults":{"compaction":{"reserveTokens":32}}}}` + "\n")
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	capsule, err := mgr.prepareOpenClaw81Config(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.OriginalSHA256 == "" || capsule.TargetSHA256 == "" || capsule.OriginalSHA256 == capsule.TargetSHA256 {
		t.Fatalf("unexpected capsule hashes: %+v", capsule)
	}
	normalized, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(normalized, &config); err != nil {
		t.Fatal(err)
	}
	cron := config["cron"].(map[string]any)
	if _, ok := cron["runLog"]; ok {
		t.Fatal("legacy cron.runLog survived normalization")
	}
	nodes := config["gateway"].(map[string]any)["nodes"].(map[string]any)
	if _, ok := nodes["denyCommands"]; ok {
		t.Fatal("legacy gateway.nodes.denyCommands survived normalization")
	}
	if _, ok := nodes["commands"].(map[string]any)["deny"]; !ok {
		t.Fatal("node command deny list was not migrated")
	}
	second, err := mgr.prepareOpenClaw81Config(workspace, req)
	if err != nil {
		t.Fatalf("idempotent config preparation failed: %v", err)
	}
	if second != capsule {
		t.Fatalf("idempotent config capsule changed: first=%+v second=%+v", capsule, second)
	}
	restored, err := mgr.restoreOpenClawConfig(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("config capsule was not restored")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("restored config differs from original\ngot: %s\nwant: %s", got, original)
	}
}

func TestUpgradeStateCapsuleRestoresOnlyControlStateAndDoctorWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-state", InstanceID: 25, UserID: 8, Generation: 1, UID: os.Geteuid(), GID: os.Getegid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	openClawDir := filepath.Join(workspace, "home", ".openclaw")
	stateFile := filepath.Join(openClawDir, "state", "openclaw.sqlite")
	cronFile := filepath.Join(openClawDir, "cron", "jobs.json")
	execApprovals := filepath.Join(openClawDir, "exec-approvals.json")
	deviceIdentity := filepath.Join(openClawDir, "identity", "device.json")
	agentControl := filepath.Join(openClawDir, "agents", "main", "agent", "openclaw-agent.sqlite")
	heartbeat := filepath.Join(openClawDir, "workspace", "HEARTBEAT.md")
	workspaceState := filepath.Join(openClawDir, "workspace", ".openclaw", "workspace-state.json")
	project := filepath.Join(workspace, "project", "keep.txt")
	for path, data := range map[string][]byte{stateFile: []byte("old-state"), cronFile: []byte("old-cron"), execApprovals: []byte("old-approvals"), deviceIdentity: []byte("old-device"), agentControl: []byte("old-agent-control"), heartbeat: []byte("old-heartbeat"), workspaceState: []byte("old-workspace-state"), project: []byte("user-project")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	capsule, err := mgr.prepareOpenClawStateCapsule(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.SchemaVersion != 2 || capsule.TotalBytes == 0 || !capsule.ControlDirs["state"] || !capsule.ControlDirs["cron"] || !capsule.ControlDirs["identity"] || !capsule.ControlDirs["agents/main/agent"] || capsule.ControlFiles["exec-approvals.json"].SHA256 == "" || !capsule.WorkspaceFiles["HEARTBEAT.md"] || !capsule.WorkspaceFiles[filepath.Join(".openclaw", "workspace-state.json")] {
		t.Fatalf("capsule = %+v", capsule)
	}
	if err := os.WriteFile(stateFile, []byte("new-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cronFile, []byte("new-cron"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(heartbeat, []byte("new-heartbeat"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{execApprovals: []byte("new-approvals"), deviceIdentity: []byte("new-device"), agentControl: []byte("new-agent-control"), workspaceState: []byte("new-workspace-state"), filepath.Join(openClawDir, "new-doctor-state.json"): []byte("target-only")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := mgr.restoreOpenClawStateCapsule(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("state capsule was not restored")
	}
	for path, want := range map[string]string{stateFile: "old-state", cronFile: "old-cron", execApprovals: "old-approvals", deviceIdentity: "old-device", agentControl: "old-agent-control", heartbeat: "old-heartbeat", workspaceState: "old-workspace-state", project: "user-project"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(openClawDir, "new-doctor-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target-only Doctor state was not quarantined: %v", err)
	}
}

func TestUpgradeCompatibilityProbeIncludesTopLevelDoctorState(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-probe", InstanceID: 26, UserID: 8, Generation: 1, UID: os.Getuid(), GID: os.Getgid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	openClawDir := filepath.Join(workspace, "home", ".openclaw")
	for path, data := range map[string][]byte{
		filepath.Join(openClawDir, "exec-approvals.json"):     []byte(`{"version":1}`),
		filepath.Join(openClawDir, "identity", "device.json"): []byte(`{"version":1}`),
		filepath.Join(openClawDir, "openclaw.json"):           []byte(`{"agents":{"defaults":{"workspace":"` + filepath.ToSlash(filepath.Join(openClawDir, "workspace")) + `"}}}`),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probeRoot, probeHome, _, err := mgr.createUpgradeCompatibilityProbe(workspace, map[string]any{}, req)
	if err != nil {
		t.Fatal(err)
	}
	defer removeUpgradeProbe(probeRoot)
	for _, rel := range []string{"exec-approvals.json", filepath.Join("identity", "device.json")} {
		if _, err := os.Stat(filepath.Join(probeHome, ".openclaw", rel)); err != nil {
			t.Fatalf("probe omitted %s: %v", rel, err)
		}
	}
}

func TestSchemaV1StateCapsuleDoesNotRemoveNewerWorkspaceStateInputs(t *testing.T) {
	root := t.TempDir()
	mgr := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: OpenClaw81Version, WorkspaceRoot: root, PodUID: "pod-target"}, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "rollout-v1-compat", InstanceID: 27, UserID: 8, Generation: 1, UID: os.Getuid(), GID: os.Getgid()}
	workspace, err := mgr.workspaceForUpgrade(req)
	if err != nil {
		t.Fatal(err)
	}
	workspaceState := filepath.Join(workspace, "home", ".openclaw", "workspace", ".openclaw", "workspace-state.json")
	if err := os.MkdirAll(filepath.Dir(workspaceState), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceState, []byte("pre-existing-v1-untracked-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	capsuleDir := mgr.configCapsuleDir(req)
	if err := os.MkdirAll(capsuleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(upgradeStateCapsule{
		SchemaVersion:  1,
		InstanceID:     req.InstanceID,
		WorkspaceFiles: map[string]bool{"HEARTBEAT.md": false, "TOOLS.md": false},
		ControlDirs:    map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capsuleDir, "state-capsule.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := mgr.restoreOpenClawStateCapsule(workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("schema v1 capsule was not accepted")
	}
	got, err := os.ReadFile(workspaceState)
	if err != nil || string(got) != "pre-existing-v1-untracked-state" {
		t.Fatalf("schema v1 rollback modified newer workspace state: %q, %v", got, err)
	}
}

func TestRemapProbeWorkspacePathsCannotReachLiveWorkspace(t *testing.T) {
	live := filepath.Join(t.TempDir(), "live-workspace")
	probe := filepath.Join(t.TempDir(), ".openclaw")
	config := map[string]any{"agents": map[string]any{"defaults": map[string]any{"workspace": live}, "list": []any{map[string]any{"id": "worker", "workspace": live + "-worker"}}}}
	copies := remapProbeWorkspacePaths(config, probe)
	defaults := config["agents"].(map[string]any)["defaults"].(map[string]any)
	mapped := defaults["workspace"].(string)
	if !pathWithin(probe, mapped) || mapped == live || copies[live] != mapped {
		t.Fatalf("workspace remap escaped probe: mapped=%q copies=%v", mapped, copies)
	}
}

func TestProbeControlPathsPreserveCustomStoreLayoutWithoutLiveReferences(t *testing.T) {
	state := filepath.Join(t.TempDir(), "home", ".openclaw")
	probe := filepath.Join(t.TempDir(), "home", ".openclaw")
	config := map[string]any{
		"agents":  map[string]any{"entries": map[string]any{"worker": map[string]any{"agentDir": filepath.Join(state, "custom", "worker")}}},
		"session": map[string]any{"store": filepath.Join(state, "stores", "{agentId}", "sessions.json")},
	}
	if err := remapProbeControlPaths(config, state, probe); err != nil {
		t.Fatal(err)
	}
	if got := config["session"].(map[string]any)["store"]; got != filepath.Join(probe, "stores", "{agentId}", "sessions.json") {
		t.Fatalf("store=%v", got)
	}
	entry := config["agents"].(map[string]any)["entries"].(map[string]any)["worker"].(map[string]any)
	if entry["agentDir"] != filepath.Join(probe, "custom", "worker") {
		t.Fatalf("agentDir=%v", entry["agentDir"])
	}
	entry["agentDir"] = filepath.Join("home", ".openclaw", "relative-control")
	config["session"].(map[string]any)["store"] = filepath.Join("home", ".openclaw", "stores", "{agentId}", "sessions.json")
	if err := remapProbeControlPaths(config, state, probe); err != nil {
		t.Fatal(err)
	}
	if entry["agentDir"] != filepath.Join(probe, "relative-control") {
		t.Fatalf("relative agentDir=%v", entry["agentDir"])
	}
	config["session"].(map[string]any)["store"] = filepath.Join(t.TempDir(), "other-owner", "sessions.json")
	if err := remapProbeControlPaths(config, state, probe); err == nil {
		t.Fatal("preflight must not touch unmanaged external state")
	}
}
