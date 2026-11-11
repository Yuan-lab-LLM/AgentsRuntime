package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkbenchProcessesRequirePinnedNamespaceUIDAndStart(t *testing.T) {
	root := t.TempDir()
	ns := filepath.Join(root, "owned-namespace")
	os.WriteFile(ns, []byte("ns"), 0600)
	f, _ := os.Open(ns)
	defer f.Close()
	info, _ := f.Stat()
	tracker := &Tracker{procRoot: root, captured: true, identity: ProcessIdentity{PID: 100, StartTime: 10}, namespaceFile: f, namespaceInfo: info, containerLifecycle: true, businessUID: 200001}
	proc := func(pid int, uid string, namespace string, start int) {
		dir := filepath.Join(root, fmt.Sprint(pid))
		os.MkdirAll(filepath.Join(dir, "ns"), 0700)
		os.Symlink(namespace, filepath.Join(dir, "ns", "user"))
		os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (service) S 100 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 %d 0", pid, start)), 0600)
		os.WriteFile(filepath.Join(dir, "status"), []byte("Uid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n"), 0600)
		os.WriteFile(filepath.Join(dir, "comm"), []byte("server\n"), 0600)
	}
	proc(100, "200001", ns, 10)
	proc(101, "200001", ns, 11)
	proc(102, "200002", ns, 12)
	other := filepath.Join(root, "foreign")
	os.WriteFile(other, []byte("other"), 0600)
	proc(103, "200001", other, 13)
	list, err := tracker.WorkbenchProcesses(context.Background(), 200001)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Identity.PID != 101 {
		t.Fatalf("foreign process admitted: %+v", list)
	}
	if err := tracker.StopWorkbenchProcess(context.Background(), 200001, ProcessIdentity{PID: 101, StartTime: 99}); err == nil {
		t.Fatal("stale start identity allowed")
	}
	if err := tracker.StopWorkbenchProcess(context.Background(), 200001, ProcessIdentity{PID: 103, StartTime: 13}); err == nil {
		t.Fatal("foreign namespace allowed")
	}
}

func TestWorkbenchCleanupRejectsUnknownPositivelyOwnedTask(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scope")
	os.WriteFile(path, []byte("mnt"), 0600)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, _ := file.Stat()
	dir := filepath.Join(root, "123")
	os.MkdirAll(filepath.Join(dir, "ns"), 0700)
	os.Symlink(path, filepath.Join(dir, "ns/mnt"))
	os.WriteFile(filepath.Join(dir, "status"), []byte("Uid:\t200002\t200002\t200002\t200002\n"), 0600)
	os.WriteFile(filepath.Join(dir, "stat"), []byte("unreadable identity"), 0600)
	tracker := &Tracker{procRoot: root, namespaceFile: file, namespaceInfo: info, auxiliaryMountScope: true, containerLifecycle: true, businessUID: 200002}
	if err := tracker.StopWorkbenchScope(context.Background(), 200002); err == nil {
		t.Fatal("unknown task in owned mount scope was reported stopped")
	}
}
