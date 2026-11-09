package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func prepareTeamTestWorkspace(root, kind string, req gateway.CreateGatewayRequest) (string, error) {
	workspace, err := gateway.PrepareWorkspace(root, kind, req)
	if err != nil {
		return "", err
	}
	return workspace, prepareTeamWorkspace(gateway.Config{WorkspaceRoot: root}, req, workspace)
}
func TestHermesLiteTeamEnvironmentAndConfigMatchOpenClawContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Hermes Team workspace aliases require Unix symbolic links")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-2")
	shared := filepath.Join(root, "teams", "user-1", "team-54-shared")
	req := gateway.CreateGatewayRequest{
		AgentType:     "hermes",
		InstanceID:    2,
		UserID:        1,
		WorkspacePath: workspace,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":     "true",
			"CLAWMANAGER_TEAM_ID":          "54",
			"CLAWMANAGER_TEAM_MEMBER_ID":   "developer",
			"CLAWMANAGER_TEAM_CONFIG_JSON": `{"teamId":"54","memberId":"developer"}`,
			"CLAWMANAGER_TEAM_SHARED_DIR":  shared,
		},
	}

	if _, _, ok := gateway.LiteTeamEnvironment(req, workspace); !ok {
		t.Fatal("Hermes Lite Team environment was not enabled")
	}
	if _, err := prepareTeamTestWorkspace(root, "hermes", req); err != nil {
		t.Fatalf("PrepareWorkspace() error = %v", err)
	}
	if err := gateway.WriteLiteTeamConfigJSON(req, workspace); err != nil {
		t.Fatalf("gateway.WriteLiteTeamConfigJSON() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "team.json")); err != nil {
		t.Fatalf("Hermes Team config missing: %v", err)
	}
	for _, alias := range []string{
		filepath.Join(workspace, "home", ".hermes", "team"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "team"),
	} {
		target, err := os.Readlink(alias)
		if err != nil {
			t.Fatalf("read Hermes Team alias %s: %v", alias, err)
		}
		if filepath.Clean(target) != filepath.Clean(shared) {
			t.Fatalf("Hermes Team alias = %q want %q", target, shared)
		}
	}
	for _, directory := range []string{
		filepath.Join(workspace, "home", ".clawmanager-team-worker"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "runtime"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".cache", "npm"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".cache", "uv"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".config"),
		filepath.Join(workspace, "home", ".clawmanager-team-worker", ".local", "share"),
	} {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("Hermes Team worker directory %s missing: %v", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			t.Fatalf("Hermes Team worker path %s is not a real directory", directory)
		}
	}
	readyFile := filepath.Join(
		workspace,
		"home",
		".clawmanager-team-worker",
		".hermes",
		"runtime",
		"redis-team.ready.json",
	)
	for _, stale := range []string{readyFile, readyFile + ".failed"} {
		if err := os.WriteFile(stale, []byte(`{"ready":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prepareTeamTestWorkspace(root, "hermes", req); err != nil {
		t.Fatalf("PrepareWorkspace() stale readiness cleanup error = %v", err)
	}
	for _, stale := range []string{readyFile, readyFile + ".failed"} {
		if _, err := os.Lstat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale Hermes Team startup state was not cleared: %s (%v)", stale, err)
		}
	}
}

func TestHermesLiteTeamRejectsSymlinkedWorkerHome(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "hermes", "user-1", "instance-3")
	shared := filepath.Join(root, "teams", "user-1", "team-55-shared")
	workerParent := filepath.Join(workspace, "home")
	if err := os.MkdirAll(workerParent, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "preserve.txt")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerLink := filepath.Join(workerParent, ".clawmanager-team-worker")
	if err := os.Symlink(outside, workerLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	req := gateway.CreateGatewayRequest{
		AgentType:     "hermes",
		InstanceID:    3,
		UserID:        1,
		WorkspacePath: workspace,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_ID":         "55",
			"CLAWMANAGER_TEAM_MEMBER_ID":  "developer",
			"CLAWMANAGER_TEAM_SHARED_DIR": shared,
		},
	}
	if _, err := prepareTeamTestWorkspace(root, "hermes", req); err == nil {
		t.Fatalf("PrepareWorkspace() error = %v, want symlink rejection", err)
	}
	if target, err := os.Readlink(workerLink); err != nil || target != outside {
		t.Fatalf("worker symlink changed: target=%q error=%v", target, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 || entries[0].Name() != "preserve.txt" {
		t.Fatalf("rejected worker home modified outside directory: entries=%v error=%v", entries, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unchanged" {
		t.Fatalf("rejected worker home modified outside file: %q error=%v", data, err)
	}
}
