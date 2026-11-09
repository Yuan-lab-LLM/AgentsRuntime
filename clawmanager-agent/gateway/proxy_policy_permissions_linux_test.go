package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProxyPolicyRestrictiveUmask(t *testing.T) {
	if os.Getenv("PROXY_POLICY_UMASK_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProxyPolicyRestrictiveUmask$", "-test.v")
		cmd.Env = append(os.Environ(), "PROXY_POLICY_UMASK_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restrictive-umask child: %v\n%s", err, output)
		}
		return
	}
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "existing0700"}[existing], func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "proxy-reader-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			if err := os.Chmod(root, 0755); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "policy")
			if existing {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(dir, "policy.json")
			store := NewProxyPolicyStore(file, "tenant")
			p, err := store.Update(ProxyPolicyUpdate{Namespace: "tenant", Proxies: []string{"10.1.2.3"}})
			if err != nil {
				t.Fatal(err)
			}
			for path, mode := range map[string]os.FileMode{dir: 0755, file: 0644} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("%s: mode=%v err=%v, want %o", path, info, err, mode)
				}
			}
			if os.Geteuid() == 0 {
				cmd := exec.Command("/bin/sh", "-c", `test -r "$1" && test ! -w "$1" && test ! -w "$2"`, "read-policy", file, dir)
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200507, Gid: 1000, NoSetGroups: true}}
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated reader: %v %s", err, output)
				}
			}
			if _, err := store.Update(ProxyPolicyUpdate{Namespace: "tenant", ExpectedRevision: p.Revision, Proxies: []string{"10.1.2.4"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProxyPolicyRejectsUnsafeDirectoryWithoutChangingIt(t *testing.T) {
	for _, kind := range []string{"writable", "symlink", "parent-symlink", "wrong-owner"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "wrong-owner" && os.Geteuid() != 0 {
				t.Skip("requires isolated root fixture")
			}
			root := t.TempDir()
			dir := filepath.Join(root, "policy")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "writable":
				if err := os.Chmod(dir, 0777); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(dir, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(root, "alias")
			case "parent-symlink":
				if err := os.Symlink(dir, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(root, "alias", "nested")
			case "wrong-owner":
				if err := os.Chown(dir, 200507, 1000); err != nil {
					t.Fatal(err)
				}
			}
			store := NewProxyPolicyStore(filepath.Join(dir, "policy.json"), "tenant")
			if _, err := store.Update(ProxyPolicyUpdate{Namespace: "tenant", Proxies: []string{"10.1.2.3"}}); err == nil {
				t.Fatal("unsafe directory accepted")
			}
			if store.Snapshot().Revision != "" {
				t.Fatal("failed update published policy")
			}
			if _, err := os.Lstat(filepath.Join(root, "policy", "policy.json")); !os.IsNotExist(err) {
				t.Fatal("unsafe target changed")
			}
		})
	}
}
