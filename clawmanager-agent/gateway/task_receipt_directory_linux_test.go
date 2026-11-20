//go:build linux

package gateway

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenClawReceiptLinuxOwnershipAndReuse(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 200008, 200008
	}
	if err := prepareReceiptDirectory(root, "instance", uid, gid); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "instance")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || info.Mode().Perm() != 0700 {
		t.Fatal("wrong instance permissions")
	}
	if err := prepareReceiptDirectory(root, "instance", uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0777); err != nil {
		t.Fatal(err)
	}
	if prepareReceiptDirectory(root, "instance", uid, gid) == nil {
		t.Fatal("insecure existing directory reused")
	}
	info, _ = os.Stat(target)
	if info.Mode().Perm() != 0777 {
		t.Fatal("existing data permissions silently rewritten")
	}
}

func TestOpenClawReceiptLinuxRejectsUnsafeRootAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if prepareReceiptDirectory(root, "instance", os.Geteuid(), os.Getegid()) == nil {
		t.Fatal("world writable root accepted")
	}
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "instance")); err != nil {
		t.Fatal(err)
	}
	if prepareReceiptDirectory(root, "instance", os.Geteuid(), os.Getegid()) == nil {
		t.Fatal("instance symlink accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if prepareReceiptDirectory(alias, "next", os.Geteuid(), os.Getegid()) == nil {
		t.Fatal("symlink root accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("outside target modified")
	}
}
