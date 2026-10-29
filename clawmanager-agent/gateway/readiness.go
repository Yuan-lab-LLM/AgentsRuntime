package gateway

import (
	"context"
	"time"
)

// A slot must attribute the listening process before sending business
// credentials. Native CheckHealth includes the runtime's readiness contract.
func (m *GatewayManager) waitGatewayReady(ctx context.Context, spec GatewayStartSpec, process ManagedProcess) error {
	if process.VerifyListeners == nil {
		return m.health.WaitReady(ctx, spec)
	}
	timeout := m.cfg.GatewayStartupTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := readyCtx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		if lastErr = process.VerifyListeners(readyCtx); lastErr == nil {
			probeCtx, stop := context.WithTimeout(readyCtx, 5*time.Second)
			lastErr = m.health.CheckHealth(probeCtx, spec)
			stop()
			if lastErr == nil {
				lastErr = process.VerifyListeners(readyCtx)
				if lastErr == nil {
					return nil
				}
			}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			if lastErr != nil {
				return lastErr
			}
			return readyCtx.Err()
		case <-timer.C:
		}
	}
}
