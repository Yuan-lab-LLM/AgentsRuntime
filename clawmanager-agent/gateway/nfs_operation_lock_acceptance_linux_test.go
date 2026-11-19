//go:build linux

package gateway

import (
	"errors"
	"os"
	"testing"
)

// Invoked by the CM integration test as management UID10001 against the same
// root-squashed NFS mount. This exercises the actual Runtime operation fence.
func TestNFSBackendOperationLockProbe(t *testing.T) {
	root := os.Getenv("CM_NFS_OPERATION_ROOT")
	if root == "" {
		t.Skip("requires isolated CM/NFS lock acceptance")
	}
	manager := NewGatewayManager(Config{WorkspaceRoot: root}, &upgradeTestStarter{}, nil)
	release, err := manager.lockUpgradeOperation(12)
	if os.Getenv("CM_NFS_OPERATION_EXPECT_BUSY") == "1" {
		if err == nil {
			release()
			t.Fatal("Runtime admitted while CM held transferred lock")
		}
		if !errors.Is(err, ErrWriterLeaseActive) {
			t.Fatalf("not the shared operation fence: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("CM release did not free Runtime operation fence: %v", err)
	}
	release()
}
