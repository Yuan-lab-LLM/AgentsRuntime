package openclaw

import (
	"context"
	"errors"
	"time"
)

// Maintenance runs image-local Doctor/SQLite/metadata commands in a fresh,
// disconnected NET. Its authority is the authenticated rollout operation and
// writer lease; no running-gateway credential or outbound grant is borrowed.
func (m *upgradeService) protectedMaintenanceContext(ctx context.Context, req WorkspaceUpgradeRequest, readOnly bool) (context.Context, func(), error) {
	fail := errors.New("protected maintenance operation unavailable")
	if m.cfg.NetworkSecurityProfile != "isolated-network-v1" || !m.cfg.IsolatedLifecycle || m.cfg.PodUID == "" || m.host == nil || m.io == nil || m.io.commandUncertain || req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.UID <= 0 || req.GID <= 0 || validateUpgradeID("rollout_id", req.RolloutID) != nil || ctx.Err() != nil {
		return nil, nil, fail
	}
	op := m.io.operation
	if op.InstanceID != req.InstanceID || op.UserID != req.UserID || op.Generation != req.Generation || op.RolloutID != req.RolloutID || op.UID != req.UID || op.GID != req.GID || op.LeaseToken != req.LeaseToken {
		return nil, nil, fail
	}
	if readOnly {
		// The private preflight marker selects RO live data plus operation-owned
		// writable capsules. upgradeSandboxSpec enforces every mount and argument.
		return ctx, func() {}, nil
	}
	if m.host.VerifyUpgradeWriter(ctx, req) != nil {
		return nil, nil, fail
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				check, finish := context.WithTimeout(child, 2*time.Second)
				deadlineDone := make(chan struct{})
				stopDeadline := context.AfterFunc(check, func() {
					defer close(deadlineDone)
					cancel()
				})
				err := m.host.VerifyUpgradeWriter(check, req)
				if !stopDeadline() {
					<-deadlineDone
				}
				if check.Err() != nil {
					err = check.Err()
				}
				finish()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return child, func() { cancel(); <-done }, nil
}
