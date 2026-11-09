//go:build linux || darwin

package slots

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func tmpFixture(t *testing.T) (gateway.Config, gateway.GatewayStartSpec) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 200001, 200001
	}
	return gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw"}, gateway.GatewayStartSpec{InstanceID: 2, UserID: 1, UID: uid, GID: gid, Generation: 1}
}

func TestPersistentTmpStableIdentityAndMetadata(t *testing.T) {
	cfg, spec := tmpFixture(t)
	first, err := prepareInstanceTmp(cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2", "tmp")
	if first != want {
		t.Fatalf("path=%s", first)
	}
	file := filepath.Join(first, "retained")
	if err := os.WriteFile(file, []byte("instance bytes"), 0640); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	spec.Generation = 5
	cfg.AgentDataDir = filepath.Join(cfg.WorkspaceRoot, "another-agent")
	again, err := prepareInstanceTmp(cfg, spec)
	if err != nil || again != first {
		t.Fatalf("unstable: %s %v", again, err)
	}
	after, _ := os.Stat(file)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "instance bytes" || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("existing tmp file changed")
	}
	for _, kind := range []string{"openclaw", "hermes", "opencode", "deepseek-harness"} {
		for _, identity := range [][2]int{{1, 2}, {1, 3}, {2, 2}} {
			cfg.RuntimeType = kind
			spec.UserID, spec.InstanceID = identity[0], identity[1]
			path, err := prepareInstanceTmp(cfg, spec)
			if err != nil {
				t.Fatal(err)
			}
			if path != first {
				if _, err := os.Stat(filepath.Join(path, "retained")); !os.IsNotExist(err) {
					t.Fatal("sibling saw tmp")
				}
			}
		}
	}
}

func TestPersistentTmpRejectsUnsafePaths(t *testing.T) {
	for _, caseName := range []string{"root-symlink", "ancestor-symlink", "leaf-symlink", "writable-parent", "wrong-owner", "wrong-parent-owner", "wrong-leaf-mode", "runtime-traversal", "zero-instance", "zero-uid"} {
		t.Run(caseName, func(t *testing.T) {
			cfg, spec := tmpFixture(t)
			if (caseName == "wrong-owner" || caseName == "wrong-parent-owner") && os.Geteuid() != 0 {
				t.Skip("actual ownership change requires root")
			}
			path, err := prepareInstanceTmp(cfg, spec)
			if err != nil {
				t.Fatal(err)
			}
			switch caseName {
			case "root-symlink":
				alias := cfg.WorkspaceRoot + "-alias"
				if err := os.Symlink(cfg.WorkspaceRoot, alias); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(alias)
				cfg.WorkspaceRoot = alias
			case "ancestor-symlink":
				parent := filepath.Dir(path)
				if err := os.Rename(parent, parent+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-real", parent); err != nil {
					t.Fatal(err)
				}
			case "leaf-symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "writable-parent":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				if err := os.Chown(path, spec.UID+1, spec.GID); err != nil {
					t.Fatal(err)
				}
			case "wrong-parent-owner":
				if err := os.Chown(filepath.Dir(path), spec.UID, spec.GID); err != nil {
					t.Fatal(err)
				}
			case "wrong-leaf-mode":
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "runtime-traversal":
				cfg.RuntimeType = "../hermes"
			case "zero-instance":
				spec.InstanceID = 0
			case "zero-uid":
				spec.UID = 0
			}
			if _, err := prepareInstanceTmp(cfg, spec); err == nil {
				t.Fatal("unsafe tmp accepted")
			}
		})
	}
}

func TestPersistentTmpBusinessPrimaryAuxAndSlotReuse(t *testing.T) {
	cfg, spec := tmpFixture(t)
	spec.WorkspacePath = filepath.Join(cfg.WorkspaceRoot, "openclaw", "user-1", "instance-2")
	spec.Command = []string{"/bin/true"}
	if err := os.MkdirAll(spec.WorkspacePath, 0700); err != nil {
		t.Fatal(err)
	}
	first, _, err := (sandboxStarter{cfg: cfg}).businessCommandSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.TmpSource == "" {
		t.Fatal("business command omitted persistent tmp")
	}
	spec.Generation++
	cfg.NetworkSlotIndex = 9
	cfg.AgentDataDir = filepath.Join(cfg.WorkspaceRoot, "another-pod")
	spec.Command = []string{"/bin/echo", "auxiliary"}
	second, _, err := (sandboxStarter{cfg: cfg}).businessCommandSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.TmpSource != first.TmpSource {
		t.Fatal("same instance tmp changed with command/generation/slot")
	}
	if err := os.Chmod(filepath.Dir(first.TmpSource), 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (sandboxStarter{cfg: cfg}).businessCommandSpec(spec); err == nil {
		t.Fatal("unsafe persistence silently fell back")
	}
}

func TestPersistentTmpConcurrentDistinctInstances(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		cfg, spec := tmpFixture(t)
		start := make(chan struct{})
		failures := make(chan error, 32)
		var workers sync.WaitGroup
		for i := 0; i < 32; i++ {
			workers.Add(1)
			go func(id int) {
				defer workers.Done()
				<-start
				own := spec
				own.InstanceID = id + 1
				_, err := prepareInstanceTmp(cfg, own)
				failures <- err
			}(i)
		}
		close(start)
		workers.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatalf("concurrent distinct instance preparation: %v", err)
			}
		}
	}
}

func TestPersistentTmpTrustedInitializingParent(t *testing.T) {
	cfg, spec := tmpFixture(t)
	parent := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-instance-tmp")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareInstanceTmp(cfg, spec); err != nil {
		t.Fatalf("trusted private initialization directory rejected: %v", err)
	}
	info, err := os.Stat(parent)
	if err != nil || info.Mode().Perm() != 0711 {
		t.Fatalf("traversal not completed: %v", err)
	}
}
