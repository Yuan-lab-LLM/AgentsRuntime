package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

// A required slot reset is a continuation, not proof of failed teardown. A
// joined real error must remain a failure even when it includes this sentinel.
func onlyContainerResetRequired(err error) bool {
	if err == sandbox.ErrContainerResetRequired {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyContainerResetRequired(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyContainerResetRequired(wrapped)
	}
	return false
}

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

// PrepareContainerForceDelete is an explicit operation, never an escalation of
// ordinary reset retries. It keeps every reservation until container teardown.
func (m *GatewayManager) PrepareContainerForceDelete(ctx context.Context, id string) error {
	if m.cfg.SandboxProfile != "nfs-rootless-v1" {
		return ErrGatewayStopConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	record := m.gateways[id]
	if record == nil {
		m.mu.Unlock()
		return nil
	}
	record.forceDelete, record.deletePending = true, true
	record.state.State = "stopping"
	record.cancel()
	process := record.process
	m.mu.Unlock()
	// Kill before joining the lifecycle owner: it may itself be waiting in a
	// graceful primary shutdown with an independent timeout.
	if process.PID != 0 {
		if process.PrepareForceReset == nil {
			return fmt.Errorf("explicit container force preparation is unavailable")
		}
		if err := process.PrepareForceReset(ctx); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-record.finished:
	}
	m.mu.RLock()
	current, cleanup := record.process, record.executionCleanup
	m.mu.RUnlock()
	// Startup can publish its process after cancellation but before finishing.
	if current.PID != 0 && current.PID != process.PID {
		if current.PrepareForceReset == nil {
			return fmt.Errorf("explicit container force preparation is unavailable")
		}
		if err := current.PrepareForceReset(ctx); err != nil {
			return err
		}
	}
	if cleanup != nil {
		return cleanup(ctx)
	}
	return nil
}
