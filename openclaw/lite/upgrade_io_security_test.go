package openclaw

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/slots"
)

// Model a non-root business identity even when the management tests run as
// root. Non-root test runners retain their identity so actual chown is allowed.
func assignedUpgradeTestIdentity() (int, int) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 {
		uid = 200007
	}
	if gid <= 0 {
		gid = 200007
	}
	return uid, gid
}

func scopedUpgradeService(t *testing.T) (*upgradeService, WorkspaceUpgradeRequest, string) {
	t.Helper()
	pool, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := assignedUpgradeTestIdentity()
	req := WorkspaceUpgradeRequest{RolloutID: "security", InstanceID: 7, UserID: 1, Generation: 1, UID: uid, GID: gid}
	workspace := filepath.Join(pool, "openclaw", "user-1", "instance-7")
	if err := os.MkdirAll(filepath.Join(workspace, "home"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{Runtime: NewProfile("openclaw"), RuntimeType: "openclaw", WorkspaceRoot: pool, OpenClawVersion: "2026.8.1"}
	manager := gateway.NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	service := &upgradeService{cfg: cfg, host: manager}
	closeIO, err := service.beginIO(req, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeIO)
	return service, req, workspace
}

func TestUpgradeScopedIORejectsSiblingAndImageWrites(t *testing.T) {
	m, req, workspace := scopedUpgradeService(t)
	sibling := filepath.Join(m.cfg.WorkspaceRoot, "openclaw", "user-2", "instance-8")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(sibling, "secret")
	if err := os.WriteFile(victim, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{victim, filepath.Join(workspace, "escape", "secret"), "/usr/cm-upgrade-must-not-create"} {
		if err := m.io.WriteFile(path, []byte("bad"), 0600); err == nil {
			t.Fatalf("write accepted %s", path)
		}
	}
	if _, err := m.io.ReadFile(filepath.Join(workspace, "escape", "secret")); err == nil {
		t.Fatal("sibling read accepted")
	}
	// A real, permitted ownership update must succeed first. Zero identities
	// would take the intentional no-op path and never exercise the boundary.
	owned := filepath.Join(workspace, "home", "owned")
	if err := m.io.WriteFile(owned, []byte("own instance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.io.Chown(owned, req.UID, req.GID); err != nil {
		t.Fatalf("own instance chown: %v", err)
	}
	if err := m.io.Chown(filepath.Join(workspace, "escape", "secret"), req.UID, req.GID); err == nil {
		t.Fatal("sibling chown accepted")
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != "untouched" {
		t.Fatalf("victim changed: %q %v", raw, err)
	}
}

func TestUpgradeScopedRenamePinsBothParents(t *testing.T) {
	m, _, workspace := scopedUpgradeService(t)
	original := filepath.Join(workspace, "original")
	target := filepath.Join(m.io.controlRoot(), "retained")
	if err := m.io.WriteFile(original, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.io.Rename(original, target); err != nil {
		t.Fatal(err)
	}
	raw, err := m.io.ReadFile(target)
	if err != nil || string(raw) != "retained" {
		t.Fatalf("retained = %q %v", raw, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := m.io.Rename(target, filepath.Join(workspace, "escape", "stolen")); err == nil {
		t.Fatal("rename into sibling accepted")
	}
	if _, err := m.io.ReadFile(target); err != nil {
		t.Fatalf("original lost: %v", err)
	}
}

func TestUpgradeCommandEnvironmentUsesExplicitSlotBase(t *testing.T) {
	t.Setenv("CLOUD_MANAGEMENT_SECRET", "must-not-leak")
	m := &upgradeService{cfg: gateway.Config{IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin", "LANG=C.UTF-8"}}}
	env := m.upgradeCommandEnv(map[string]string{"HOME": "/instance/home"})
	for _, item := range env {
		if item == "CLOUD_MANAGEMENT_SECRET=must-not-leak" {
			t.Fatal("ambient supervisor secret leaked")
		}
	}
	if len(env) != 3 {
		t.Fatalf("env = %v", env)
	}
}

func TestNativeUpgradeFacadeRejectsExternalConfigSymlink(t *testing.T) {
	pool := t.TempDir()
	workspace := filepath.Join(pool, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "controller-secret")
	if err := os.WriteFile(victim, []byte(`{"secret":"controller"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(state, "openclaw.json")); err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{Runtime: NewProfile("openclaw"), RuntimeType: "openclaw", WorkspaceRoot: pool, OpenClawVersion: "2026.8.1"}
	manager := gateway.NewGatewayManager(cfg, &upgradeTestStarter{}, nil)
	req := WorkspaceUpgradeRequest{RolloutID: "security", InstanceID: 7, UserID: 1, Generation: 1, UID: os.Getuid(), GID: os.Getgid()}
	if req.UID <= 0 {
		req.UID = 1
	}
	if req.GID <= 0 {
		req.GID = 1
	}
	if _, err := manager.PreflightUpgradeCompatibility(context.Background(), req); err == nil || !strings.Contains(err.Error(), "read OpenClaw config") {
		t.Fatalf("native facade external config error = %v", err)
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != `{"secret":"controller"}` {
		t.Fatalf("victim changed: %q %v", raw, err)
	}
}

func TestNativeMutationOwnsWriterAcrossManagementIO(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "uncertain"}[uncertain], func(t *testing.T) {
			m, req, workspace := scopedUpgradeService(t)
			m.cfg.IsolatedLifecycle = true
			m.cfg.PodUID = "maintenance"
			dir := filepath.Join(m.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "openclaw-1-7")
			if err := os.WriteFile(base+".lock", nil, 0600); err != nil {
				t.Fatal(err)
			}
			ledger := []byte(`{"instance_id":7,"user_id":1,"generation":1,"runtime_type":"openclaw","pod_uid":"previous","slot":"slot-0","state":"stopped"}`)
			if err := os.WriteFile(base+".json", ledger, 0600); err != nil {
				t.Fatal(err)
			}
			finish, err := m.beginMutationIO(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := slots.AcquireStoppedCommandWriter(m.cfg, req); err == nil {
				t.Fatal("management IO ran without exclusive writer fence")
			}
			if err := m.io.WriteFile(filepath.Join(workspace, "managed"), []byte("done"), 0600); err != nil {
				t.Fatal(err)
			}
			m.io.commandUncertain = uncertain
			if uncertain {
				if err := m.io.WriteFile(filepath.Join(workspace, "managed"), []byte("raced rollback"), 0600); err == nil {
					t.Fatal("management write continued after uncertain teardown")
				}
			}
			err = finish()
			if uncertain && err == nil {
				t.Fatal("unconfirmed teardown returned success")
			}
			if !uncertain && err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(base + ".json")
			expected := `"state":"stopped"`
			if uncertain {
				expected = `"state":"upgrading"`
			}
			if err != nil || !strings.Contains(string(raw), expected) {
				t.Fatalf("ledger = %s %v", raw, err)
			}
		})
	}
}
