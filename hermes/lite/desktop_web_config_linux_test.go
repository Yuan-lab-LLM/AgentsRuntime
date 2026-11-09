//go:build linux

package hermes

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDesktopWebConfigTraversableParentsWithPrivateUmask(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to verify traversal and isolation as distinct instance UIDs")
	}
	for _, fixture := range []struct {
		name     string
		existing os.FileMode
		want     os.FileMode
	}{
		{"fresh", 0, 0o755},
		{"repair_0700", 0o700, 0o711},
		{"preserve_0711", 0o711, 0o711},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cfg, req, _ := desktopWebConfigFixture(t)
			// A direct /tmp child avoids unrelated 0700 testing-directory
			// ancestors when the child process switches to the instance UID.
			root, err := os.MkdirTemp("/tmp", "hermes-parent-permissions-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if err := os.Chmod(root, 0o755); err != nil {
				t.Fatal(err)
			}
			cfg.WorkspaceRoot = root
			workspace := filepath.Join(root, "hermes", "user-45", "instance-63")
			req.WorkspacePath, req.UID, req.GID = workspace, 246810, 246811
			parents := []string{filepath.Join(root, "hermes"), filepath.Join(root, "hermes", "user-45")}
			if fixture.existing != 0 {
				for _, parent := range parents {
					if err := os.Mkdir(parent, fixture.existing); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(parent, fixture.existing); err != nil {
						t.Fatal(err)
					}
				}
			}
			previousUmask := syscall.Umask(0o077)
			defer syscall.Umask(previousUmask)
			if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
				t.Fatal(err)
			}
			sibling := req
			sibling.InstanceID, sibling.UID, sibling.GID = 64, 246812, 246813
			sibling.WorkspacePath = filepath.Join(root, "hermes", "user-45", "instance-64")
			if err := WriteGatewayConfig(cfg, sibling, sibling.WorkspacePath); err != nil {
				t.Fatal(err)
			}
			for _, parent := range parents {
				info, err := os.Stat(parent)
				if err != nil {
					t.Fatal(err)
				}
				owner := info.Sys().(*syscall.Stat_t)
				if info.Mode().Perm() != fixture.want || owner.Uid != 0 || owner.Gid != uint32(os.Getgid()) {
					t.Fatalf("shared parent mode/owner changed incorrectly: %s mode=%o uid=%d gid=%d", filepath.Base(parent), info.Mode().Perm(), owner.Uid, owner.Gid)
				}
			}
			for _, name := range []string{"", "home", "home/.hermes"} {
				info, err := os.Stat(filepath.Join(workspace, name))
				if err != nil {
					t.Fatal(err)
				}
				owner := info.Sys().(*syscall.Stat_t)
				if info.Mode().Perm() != 0o750 || owner.Uid != uint32(req.UID) || owner.Gid != uint32(req.GID) {
					t.Fatal("instance directory permissions or ownership changed")
				}
			}
			// Enter the actual workspace after setuid/setgid, read only access
			// checks, and prove the sibling instance remains inaccessible.
			command := exec.Command("/bin/sh", "-c", `test -r home/.hermes/.env && test -w home/.hermes/config.yaml && test ! -x "$1" && test ! -r "$1/home/.hermes/.env"`, "instance-access-check", sibling.WorkspacePath)
			command.Dir = workspace
			command.Env = []string{"PATH=/usr/bin:/bin"}
			command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(req.UID), Gid: uint32(req.GID)}}
			if err := command.Run(); err != nil {
				t.Fatalf("instance cannot enter/read its workspace, or sibling isolation failed: %v", err)
			}
		})
	}
}

func TestDesktopWebConfigAssignsInstanceOwnershipBeforeRename(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to verify ownership for a distinct instance UID")
	}
	cfg, req, workspace := desktopWebConfigFixture(t)
	req.UID, req.GID = 246810, 246811
	hermesHome := filepath.Join(workspace, "home", ".hermes")
	if err := os.MkdirAll(hermesHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hermesHome, ".env"), []byte("CUSTOM=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{workspace, filepath.Join(workspace, "home"), hermesHome, filepath.Join(hermesHome, ".env"), filepath.Join(hermesHome, "config.yaml"), filepath.Join(hermesHome, "gateway.json"), filepath.Join(hermesHome, desktopWebMarkerFile), filepath.Join(hermesHome, ".env.clawmanager-pre-desktop-web.bak")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Uid != uint32(req.UID) || owner.Gid != uint32(req.GID) {
			t.Fatalf("wrong instance ownership for %s: %d:%d", filepath.Base(path), owner.Uid, owner.Gid)
		}
		if !info.IsDir() && info.Mode().Perm() != 0o600 {
			t.Fatalf("insecure managed file mode: %s", filepath.Base(path))
		}
	}
}
