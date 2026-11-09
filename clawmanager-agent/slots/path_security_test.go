package slots

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagementStorageRejectsWritableDirectoryAndSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "management")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := trustedDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := trustedDirectory(dir); err == nil {
		t.Fatal("untrusted writer directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lock")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := trustedFile(link); err == nil {
		t.Fatal("symlinked writer lock accepted")
	}
}
