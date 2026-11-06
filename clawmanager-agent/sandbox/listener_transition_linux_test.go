//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestListenersRejectChangedTableDespiteInitiallyCompleteAttribution(t *testing.T) {
	tracker, _, _ := listenerFixture(t)
	defer tracker.Close()
	path := filepath.Join(tracker.procRoot, "123", "fd")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	table := filepath.Join(tracker.procRoot, "123", "net", "tcp")
	initial, err := os.ReadFile(table)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(table); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(table, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		// Opening the FIFO proves the first snapshot has opened its old file.
		// Replace the path before publishing that snapshot, so the final read
		// deterministically sees a new, unattributed listener identity.
		writer, err := os.OpenFile(table, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		defer writer.Close()
		next := table + ".next"
		err = os.WriteFile(next, []byte(strings.ReplaceAll(string(initial), "0 111", "0 999")), 0600)
		if err == nil {
			err = os.Rename(next, table)
		}
		if err == nil {
			_, err = writer.Write(initial)
		}
		done <- err
	}()
	err = tracker.VerifyListeners(context.Background(), 20000, 1)
	if writeErr := <-done; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "listener identity changed") {
		t.Fatalf("changed final listener table did not fail ownership proof: %v", err)
	}
}
