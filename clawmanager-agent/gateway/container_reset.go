package gateway

import (
	"context"
	"fmt"
)

// PrepareContainerReset fences admission, joins application/automation work and
// closes execution resources held outside the business process. It deliberately
// retains reservations: the caller still needs actual container termination.
func (m *GatewayManager) PrepareContainerReset(ctx context.Context, id string, graceful bool, persistGraceful func() error) error {
	if m.cfg.SandboxProfile != "nfs-rootless-v1" {
		return ErrGatewayStopConflict
	}
	m.mu.RLock()
	record := m.gateways[id]
	m.mu.RUnlock()
	if record == nil {
		if graceful {
			return ErrGatewayStopConflict
		}
		// The worker may have durably claimed its writer before native storage
		// preparation failed. Container teardown is still required for reuse.
		return nil
	}
	if graceful {
		req := IdleStopRequest{InstanceID: record.state.InstanceID, Generation: record.state.Generation, PodUID: m.cfg.PodUID}
		// The ordinary idle method must retain its whole-execution proof contract.
		// Its accepted fence is reused here, while PrepareReset below proves only
		// graceful primary completion before the container cleanup boundary.
		idleErr := m.IdleStopGateway(ctx, id, req, persistGraceful)
		m.mu.RLock()
		accepted := record.idleStop
		m.mu.RUnlock()
		if !accepted {
			return idleErr
		}
	} else {
		m.mu.Lock()
		if record.idleStop {
			m.mu.Unlock()
			return ErrGatewayStopConflict
		}
		record.deletePending = true
		record.state.State = "stopping"
		record.cancel()
		m.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-record.finished:
	}
	m.mu.RLock()
	process, cleanup := record.process, record.executionCleanup
	m.mu.RUnlock()
	if process.PID != 0 {
		if process.PrepareReset == nil {
			return fmt.Errorf("container reset preparation is unavailable")
		}
		if err := process.PrepareReset(ctx, graceful); err != nil {
			return err
		}
	}
	if cleanup != nil {
		return cleanup(ctx)
	}
	return nil
}
