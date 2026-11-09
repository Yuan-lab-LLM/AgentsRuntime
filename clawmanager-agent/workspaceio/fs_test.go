package workspaceio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedWritesAndOwnershipRejectSiblingSymlinks(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "secret")
	if err := os.WriteFile(victim, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile(filepath.Join(base, "escape", "secret"), []byte("changed"), 0644); err == nil {
		t.Fatal("sibling write allowed")
	}
	if err := root.Chmod("escape/secret", 0777); err == nil {
		t.Fatal("sibling chmod allowed")
	}
	data, _ := os.ReadFile(victim)
	info, _ := os.Stat(victim)
	if string(data) != "secret" || info.Mode().Perm() != 0600 {
		t.Fatal("sibling modified")
	}
}
func TestBoundedAtomicWriteReplacesLeafWithoutFollowingIt(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(base, "config.json"))
	root, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFileAtomic("config.json", []byte("own"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "secret" {
		t.Fatal("followed leaf symlink")
	}
	data, _ = root.ReadFile("config.json")
	if string(data) != "own" {
		t.Fatal("own config missing")
	}
}

func TestManagementRootPreparationProtectsOnlyFixedParents(t *testing.T) {
	base := t.TempDir()
	os.Chmod(base, 0777)
	nested := filepath.Join(base, "runtime", "user-1", "instance-2")
	os.MkdirAll(nested, 0700)
	if err := PrepareManagementRoots(base, "runtime", "teams"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, filepath.Join(base, "runtime"), filepath.Join(base, "teams")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("unprotected parent %s: %v %v", path, info, err)
		}
	}
	info, _ := os.Stat(nested)
	if info.Mode().Perm() != 0700 {
		t.Fatal("instance permissions changed during management init")
	}
}

func TestManagementTraversalKeepsParentsUnlistableAndLeavesDataPrivate(t *testing.T) {
	base := t.TempDir()
	control := filepath.Join(base, ".management", "rollout-1", "instance-7")
	child := filepath.Join(control, "probe")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := AllowManagementTraversal(base, control); err != nil {
		t.Fatal(err)
	}
	for current := control; current != base; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil || info.Mode().Perm() != 0711 {
			t.Fatalf("management ancestor permissions: %s %v", current, err)
		}
	}
	info, _ := os.Stat(child)
	if info.Mode().Perm() != 0700 {
		t.Fatal("private probe permissions changed")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := AllowManagementTraversal(base, filepath.Join(base, "redirect", "other")); err == nil {
		t.Fatal("management ancestor symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "other")); !os.IsNotExist(err) {
		t.Fatal("management helper escaped into sibling")
	}
}
