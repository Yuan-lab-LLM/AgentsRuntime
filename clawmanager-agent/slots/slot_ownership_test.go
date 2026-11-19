package slots

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSlotStartupWaitsForCoordinatorOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slot-0.sock.guard")
	owner, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = lockWorkspace(owner); err != nil {
		t.Fatal(err)
	}
	next, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitForSlotOwnership(ctx, next) }()
	select {
	case err = <-done:
		t.Fatalf("startup exited during coordinator finalization: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	owner.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup did not resume after finalization")
	}
}

func TestSlotOwnershipWaitCanBeCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slot-0.sock.guard")
	owner, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = lockWorkspace(owner); err != nil {
		t.Fatal(err)
	}
	next, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = waitForSlotOwnership(ctx, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
}
