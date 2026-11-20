//go:build linux

package receiptstorage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReceiptStorageLinuxBindingAndIsolation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to assign a distinct runtime UID")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	id := Identity{InstanceID: "8", OwnerID: "7", UID: 200008, GID: 200008}
	if err := Ensure(root, id, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, marker))
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(root, id, false); err != nil {
		t.Fatal(err)
	}
	other := id
	other.InstanceID = "9"
	if Ensure(root, other, true) == nil {
		t.Fatal("another instance adopted mount")
	}
	other = id
	other.OwnerID = "9"
	if Ensure(root, other, true) == nil {
		t.Fatal("another owner adopted mount")
	}
	after, _ := os.ReadFile(filepath.Join(root, marker))
	if string(before) != string(after) {
		t.Fatal("binding changed")
	}
	if err := os.Chmod(Directory(root), 0777); err != nil {
		t.Fatal(err)
	}
	if Ensure(root, id, true) == nil {
		t.Fatal("unsafe existing directory reused")
	}
	info, _ := os.Stat(Directory(root))
	if info.Mode().Perm() != 0777 {
		t.Fatal("existing permissions changed")
	}
}
func TestReceiptStorageLinuxRefusesUnboundDataAndLinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned mount")
	}
	id := Identity{InstanceID: "8", OwnerID: "7", UID: 200008, GID: 200008}
	root := t.TempDir()
	os.Chmod(root, 0711)
	existing := filepath.Join(root, "keep")
	os.WriteFile(existing, []byte("existing data"), 0600)
	if Ensure(root, id, true) == nil {
		t.Fatal("nonempty unbound mount adopted")
	}
	if data, _ := os.ReadFile(existing); string(data) != "existing data" {
		t.Fatal("existing data changed")
	}
	clean := t.TempDir()
	os.Chmod(clean, 0711)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(clean, alias); err != nil {
		t.Fatal(err)
	}
	if Ensure(alias, id, true) == nil {
		t.Fatal("symlink root accepted")
	}
	os.Chmod(clean, 0777)
	if Ensure(clean, id, true) == nil {
		t.Fatal("world writable mount accepted")
	}
}
