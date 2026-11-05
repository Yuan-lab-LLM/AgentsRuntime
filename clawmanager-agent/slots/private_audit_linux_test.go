//go:build linux

package slots

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Run alongside the real rootless bootstrap test with only outer SETUID/GID.
// A cannot resolve the path, but its narrowly inherited grant FD remains valid.
func TestNFSPrivateAuditBelowManagement0700(t *testing.T) {
	base := os.Getenv("CM_PRIVATE_AUDIT_BASE")
	if base == "" {
		t.Skip("requires a dedicated NFS management0700 fixture")
	}
	if os.Geteuid() != 10001 {
		t.Fatal("management identity required")
	}
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	grant := filepath.Join(base, "private")
	cmd := exec.Command("/bin/mkdir", "-m", "770", grant)
	if err := configureSandboxProcess(cmd, 10001, 200101, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("grant: %v %s", err, out)
	}
	denied := exec.Command("/usr/bin/stat", grant)
	if err := configureSandboxProcess(denied, 200101, 200101, nil); err != nil {
		t.Fatal(err)
	}
	if denied.Run() == nil {
		t.Fatal("instance traversed management0700 ancestor")
	}
	if err := runPrivateStorageAudit(200101, 200101, []string{grant}, false); err != nil {
		t.Fatal("pinned grant audit:", err)
	}
	info, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatal("management ancestor permissions changed")
	}
	t.Log("exact host instance audit accepted pinned grant below unchanged management0700 ancestor")
}
