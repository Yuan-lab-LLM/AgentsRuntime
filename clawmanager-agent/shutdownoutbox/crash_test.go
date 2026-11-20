package shutdownoutbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOutboxCommittedRecordSurvivesAbruptProcessExit(t *testing.T) {
	if root := os.Getenv("CM_SHUTDOWN_OUTBOX_CRASH_ROOT"); root != "" {
		q := New(root, "test-key", "crash-scope")
		if q.Put(event()) != nil {
			os.Exit(71)
		}
		os.Exit(77)
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxCommittedRecordSurvivesAbruptProcessExit$")
	cmd.Env = append(os.Environ(), "CM_SHUTDOWN_OUTBOX_CRASH_ROOT="+root)
	output, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 77 {
		t.Fatalf("unexpected crash helper: %v %s", err, output)
	}
	restored := New(root, "test-key", "crash-scope")
	items, err := restored.Pending()
	if err != nil || len(items) != 1 || items[0].Event != event() {
		t.Fatalf("committed record lost: %v %v", items, err)
	}
}
func TestOutboxLinuxLinkedAncestorAndReadOnlyRetry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux path and permission contract")
	}
	parent := t.TempDir()
	outside := t.TempDir()
	linked := filepath.Join(parent, "linked")
	if os.Symlink(outside, linked) != nil {
		t.Fatal("fixture")
	}
	if New(linked, "key", "scope").Put(event()) == nil {
		t.Fatal("linked ancestor followed")
	}
	names, err := os.ReadDir(outside)
	if err != nil || len(names) != 0 {
		t.Fatal("outside directory modified")
	}
	q := New(t.TempDir(), "key", "scope")
	if q.Put(event()) != nil {
		t.Fatal("put")
	}
	items, _ := q.Pending()
	file := filepath.Join(q.root, items[0].ID+".json")
	if os.Chmod(file, 0400) != nil {
		t.Fatal("fixture")
	}
	if q.Put(event()) == nil {
		t.Fatal("existing retry skipped file sync")
	}
	if os.Chmod(file, 0600) != nil || q.Put(event()) != nil {
		t.Fatal("file sync retry failed")
	}
}
func TestOutboxLazyInitializationPreservesLegacyReads(t *testing.T) {
	base := filepath.Join(t.TempDir(), "new", "agent")
	q := New(base, "key", "scope")
	items, err := q.Pending()
	if err != nil || len(items) != 0 {
		t.Fatal("empty queue")
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("read created a new directory")
	}
	if q.Put(event()) != nil {
		t.Fatal("first observation did not create its directory")
	}
	items, err = q.Pending()
	if err != nil || len(items) != 1 {
		t.Fatal("put failed")
	}
}
func TestOutboxDeliveryIDWireContract(t *testing.T) {
	if event().Delivery().ID != "be03a0044a9280ba19845b5200e99e638cc5dffec72991503e2d1332aa9c7825" {
		t.Fatal("delivery wire identity changed")
	}
}
