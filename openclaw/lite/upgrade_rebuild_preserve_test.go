package openclaw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeRebuildPreservesIdentityAndOriginalData(t *testing.T) {
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(filepath.Join(state, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"openclaw.json": "{}", "history.sqlite": "original-db", "workspace/result.md": "important-result"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	req := WorkspaceUpgradeRequest{RolloutID: "repair-1", InstanceID: 7, UserID: 1, Generation: 3, UID: os.Getuid(), GID: os.Getgid()}
	if req.UID == 0 {
		req.UID, req.GID = 200007, 200007
	}
	proof, err := m.rebuildPreservingState(context.Background(), workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Installed || proof.OriginalSHA256 == "" {
		t.Fatalf("missing proof %+v", proof)
	}
	for _, name := range []string{"history.sqlite", "workspace/result.md", "openclaw.json"} {
		old, e := os.ReadFile(filepath.Join(proof.OriginalPath, name))
		if e != nil {
			t.Fatal(e)
		}
		current, e := os.ReadFile(filepath.Join(state, name))
		if e != nil || string(old) != string(current) {
			t.Fatalf("data changed %s: %v", name, e)
		}
	}
	if _, err := m.rebuildPreservingState(context.Background(), workspace, req); err != nil {
		t.Fatalf("resume rebuilt twice: %v", err)
	}
}

func TestUpgradeRebuildResumesEachCutoverBoundaryAndRejectsChangedCopy(t *testing.T) {
	for _, phase := range []string{"verified-copy", "original-retained", "candidate-installed", "candidate-corrupt"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
			workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
			state := filepath.Join(workspace, "home", ".openclaw")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "history.sqlite"), []byte("unique-user-data"), 0640); err != nil {
				t.Fatal(err)
			}
			req := WorkspaceUpgradeRequest{RolloutID: "resume-rebuild", InstanceID: 7, UserID: 1, Generation: 3, UID: 200007, GID: 200007}
			dir := filepath.Join(m.configCapsuleDir(req), "preserved-rebuild")
			original, candidate := filepath.Join(dir, "original"), filepath.Join(dir, "candidate")
			if err := os.MkdirAll(candidate, 0700); err != nil {
				t.Fatal(err)
			}
			hash, err := copyPreservedRebuildTree(context.Background(), state, candidate, workspace)
			if err != nil {
				t.Fatal(err)
			}
			proof := preservedRebuild{InstanceID: 7, Generation: 3, OriginalPath: original, CandidatePath: candidate, OriginalSHA256: hash}
			raw, _ := json.Marshal(proof)
			if err := os.WriteFile(filepath.Join(dir, "receipt.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if phase == "original-retained" || phase == "candidate-installed" {
				if err := os.Rename(state, original); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "candidate-installed" {
				if err := os.Rename(candidate, state); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "candidate-corrupt" {
				os.WriteFile(filepath.Join(candidate, "history.sqlite"), []byte("changed"), 0640)
			}
			result, err := m.rebuildPreservingState(context.Background(), workspace, req)
			if phase == "candidate-corrupt" {
				if err == nil {
					t.Fatal("corrupt candidate installed")
				}
				raw, err := os.ReadFile(filepath.Join(state, "history.sqlite"))
				if err != nil || string(raw) != "unique-user-data" {
					t.Fatal("original data changed")
				}
				return
			}
			if err != nil || !result.Installed {
				t.Fatalf("crash boundary not resumed %+v %v", result, err)
			}
			for _, dir := range []string{state, original} {
				raw, err := os.ReadFile(filepath.Join(dir, "history.sqlite"))
				if err != nil || string(raw) != "unique-user-data" {
					t.Fatal("data lost", err)
				}
			}
		})
	}
}

func TestUpgradeRebuildCancellationDoesNotMoveOriginal(t *testing.T) {
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(state, 0700)
	os.WriteFile(filepath.Join(state, "important.md"), []byte("keep"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.rebuildPreservingState(ctx, workspace, WorkspaceUpgradeRequest{RolloutID: "cancel-rebuild", InstanceID: 7, UserID: 1, Generation: 1, UID: 200007, GID: 200007})
	if err == nil {
		t.Fatal("cancelled rebuild continued")
	}
	if raw, err := os.ReadFile(filepath.Join(state, "important.md")); err != nil || string(raw) != "keep" {
		t.Fatal("cancelled rebuild moved original")
	}
}

func TestUpgradeRebuildRefusesExternalSymlinkBeforeCutover(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("symlink privileges")
	}
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, &upgradeTestStarter{}, nil)
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-7")
	state := filepath.Join(workspace, "home", ".openclaw")
	os.MkdirAll(state, 0700)
	os.WriteFile(filepath.Join(state, "openclaw.json"), []byte("{}"), 0600)
	if err := os.Symlink(t.TempDir(), filepath.Join(state, "external")); err != nil {
		t.Skip(err)
	}
	_, err := m.rebuildPreservingState(context.Background(), workspace, WorkspaceUpgradeRequest{RolloutID: "repair-1", InstanceID: 7, UserID: 1, Generation: 3, UID: 200007, GID: 200007})
	if err == nil {
		t.Fatal("external data was imported")
	}
	if _, err := os.Stat(filepath.Join(state, "openclaw.json")); err != nil {
		t.Fatal("original was moved before validation")
	}
}
