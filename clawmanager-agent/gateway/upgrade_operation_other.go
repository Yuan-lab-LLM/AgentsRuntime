//go:build !linux

package gateway

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
)

func (m *GatewayManager) upgradeOperationContext(ctx context.Context, _ int) context.Context {
	return ctx
}
func inheritUpgradeOperationFence(context.Context, *exec.Cmd) {}

var localUpgradeOperations sync.Map

// Non-Linux development tests retain process-local serialization only. The
// cross-Pod fencing capability must only be advertised by Linux Runtime builds.
func (m *GatewayManager) lockUpgradeOperation(instanceID int) (func(), error) {
	key := fmt.Sprintf("%s/%d", m.upgradeRoot(), instanceID)
	if _, loaded := localUpgradeOperations.LoadOrStore(key, true); loaded {
		return nil, ErrWriterLeaseActive
	}
	return func() { localUpgradeOperations.Delete(key) }, nil
}
