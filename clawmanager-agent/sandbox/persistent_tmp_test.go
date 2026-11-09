package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentTmpMountAndMaintenanceDefault(t *testing.T) {
	spec := testSpec(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec.TmpSource = filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2", "tmp")
	if err := os.MkdirAll(spec.TmpSource, 0700); err != nil {
		t.Fatal(err)
	}
	args, err := buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgs(args, []string{"--bind", spec.TmpSource, "/tmp"}) || containsArgs(args, []string{"--tmpfs", "/tmp"}) {
		t.Fatal("persistent tmp was not bound")
	}
	for _, path := range []string{"/run", "/dev/shm"} {
		if !containsArgs(args, []string{"--tmpfs", path}) {
			t.Fatal("runtime tmpfs changed")
		}
	}
	spec.TmpSource = ""
	spec.OfflineNetwork = true
	args, err = buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil || !containsArgs(args, []string{"--tmpfs", "/tmp"}) {
		t.Fatalf("maintenance tmp changed: %v", err)
	}
}

func TestPersistentTmpRejectsInvalidSourcesAndOverlap(t *testing.T) {
	for _, scenario := range []string{"root", "relative", "symlink", "ancestor-symlink", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			spec := testSpec(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, ".clawmanager-instance-tmp", "openclaw", "user-1", "instance-2", "tmp")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			spec.TmpSource = source
			switch scenario {
			case "root":
				spec.TmpSource = root
			case "relative":
				spec.TmpSource = "tmp"
			case "symlink":
				os.Remove(source)
				if err := os.Symlink(root, source); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				parent := filepath.Dir(source)
				os.Rename(parent, parent+"-real")
				if err := os.Symlink(parent+"-real", parent); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				spec.Mounts = append(spec.Mounts, Mount{Source: root, Target: "/tmp/operation-scratch"})
			}
			if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
				t.Fatal("unsafe tmp accepted")
			}
		})
	}
}
