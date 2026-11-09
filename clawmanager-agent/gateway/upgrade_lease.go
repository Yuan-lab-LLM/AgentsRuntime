package gateway

import (
	"context"
	"errors"
	"time"
)

// The caller holds the cross-Pod operation lock until this renewal loop and
// every child command have stopped. Losing the writer identity cancels work;
// renewal never reacquires an expired or foreign lease.
func (m *GatewayManager) keepUpgradeWriterLease(ctx context.Context, req WorkspaceUpgradeRequest) (context.Context, func() error, error) {
	lease, err := m.requireWriterLease(req)
	if err != nil {
		return ctx, nil, err
	}
	ttl := lease.ExpiresAt.Sub(lease.UpdatedAt)
	if ttl < time.Minute || ttl > time.Hour {
		return ctx, nil, errors.New("invalid writer lease renewal period")
	}
	operationCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(min(ttl/3, 30*time.Second))
		defer ticker.Stop()
		for {
			select {
			case <-operationCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if _, err := m.RenewWriterLease(req, ttl); err != nil {
					cancel()
					done <- err
					return
				}
			}
		}
	}()
	return operationCtx, func() error { cancel(); return <-done }, nil
}

func (m *GatewayManager) verifyUpgradeWriter(ctx context.Context, req WorkspaceUpgradeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := m.requireWriterLease(req)
	return err
}
