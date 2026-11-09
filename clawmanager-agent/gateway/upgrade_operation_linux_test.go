//go:build linux

package gateway

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestUpgradeOperationLockFencesIndependentManagers(t *testing.T) {
	root := os.Getenv("UPGRADE_LOCK_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else {
		var err error
		root, err = os.MkdirTemp(root, "lock-fixture-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
	}
	a := NewGatewayManager(Config{WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	b := NewGatewayManager(Config{WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	release, err := a.lockUpgradeOperation(1)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := b.lockUpgradeOperation(1); err == nil {
		other()
		release()
		t.Fatal("duplicate operation admitted")
	} else if !errors.Is(err, ErrWriterLeaseActive) {
		t.Fatal(err)
	}
	other, err := b.lockUpgradeOperation(2)
	if err != nil {
		release()
		t.Fatal("independent instance blocked", err)
	}
	other()
	release()
	release, err = b.lockUpgradeOperation(1)
	if err != nil {
		t.Fatal("completed operation did not release fence", err)
	}
	release()
}

func TestUpgradeOperationFenceSurvivesParentClose(t *testing.T) {
	root := os.Getenv("UPGRADE_LOCK_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else {
		var err error
		root, err = os.MkdirTemp(root, "child-lock-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
	}
	m := NewGatewayManager(Config{WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	release, err := m.lockUpgradeOperation(1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := m.upgradeOperationContext(context.Background(), 1)
	child := exec.Command("sleep", "1")
	inheritUpgradeOperationFence(ctx, child)
	if err := child.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if next, err := m.lockUpgradeOperation(1); err == nil {
		next()
		child.Process.Kill()
		child.Wait()
		t.Fatal("child writer lost its inherited fence")
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	next, err := m.lockUpgradeOperation(1)
	if err != nil {
		t.Fatal(err)
	}
	next()
}
