package gateway

import (
	"context"
	"errors"
	"time"
)

var (
	ErrGatewayIdleUnsupported = errors.New("native execution idle proof is unsupported")
	ErrGatewayIdleBusy        = errors.New("native execution is busy")
	ErrGatewayIdleUnknown     = errors.New("native execution idle proof is unknown")
)

type IdleStopRequest struct {
	InstanceID int    `json:"instance_id"`
	Generation int    `json:"generation"`
	PodUID     string `json:"pod_uid"`
}

func (r IdleStopRequest) Valid(id, podUID string) bool {
	return r.InstanceID > 0 && r.Generation > 0 && r.PodUID != "" && r.PodUID == podUID && id == gatewayID(r.InstanceID, r.Generation)
}

type IdlePreflightResponse struct {
	IdleDefaultsPolicy
	GatewayID               string `json:"gateway_id"`
	InstanceID              int    `json:"instance_id"`
	Generation              int    `json:"generation"`
	PodUID                  string `json:"pod_uid"`
	ExecutionState          string `json:"execution_state"`
	ExecutionProofSupported bool   `json:"execution_proof_supported"`
}

func (m *GatewayManager) idleRecord(id string, req IdleStopRequest) (*gatewayRecord, error) {
	if !req.Valid(id, m.cfg.PodUID) || !m.IsolatedGatewayLifecycle() {
		return nil, ErrGatewayStopConflict
	}
	record := m.gateways[id]
	if record == nil || record.state.InstanceID != req.InstanceID || record.state.Generation != req.Generation || record.deletePending {
		return nil, ErrGatewayStopConflict
	}
	return record, nil
}

// CM decides inactivity from its durable Gateway activity window. This guard
// only closes common supervisor command admission while committing the stop.
func (m *GatewayManager) acquireIdleGuard(ctx context.Context, record *gatewayRecord) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if record.automationStopFailed {
		return nil, ErrGatewayIdleUnknown
	}
	if record.idleChecking || record.idleStop || record.activeCommands != 0 {
		return nil, ErrGatewayIdleBusy
	}
	record.idleChecking = true
	return func() { m.mu.Lock(); record.idleChecking = false; m.mu.Unlock() }, nil
}

// IdlePreflight reports a momentary observation only. IdleStopGateway always
// acquires its own admission guard; a preflight never authorizes later signaling.
func (m *GatewayManager) IdlePreflight(ctx context.Context, id string, req IdleStopRequest) (*IdlePreflightResponse, error) {
	m.mu.RLock()
	record, err := m.idleRecord(id, req)
	if err != nil {
		m.mu.RUnlock()
		return nil, err
	}
	process, state, fenced := record.process, record.state.State, record.idleStop
	assignment, startSpec := record.request, record.startSpec
	m.mu.RUnlock()
	proof := &IdlePreflightResponse{GatewayID: id, InstanceID: req.InstanceID, Generation: req.Generation, PodUID: req.PodUID, ExecutionState: "unsupported"}
	proof.IdleDefaultsPolicy = m.idleDefaultsPolicy(assignment, startSpec)
	if process.GracefulStop == nil {
		return proof, nil
	}
	proof.ExecutionProofSupported = true
	if fenced || state != "running" {
		proof.ExecutionState = "unknown"
		return proof, nil
	}
	release, err := m.acquireIdleGuard(ctx, record)
	if release != nil {
		release()
	}
	m.mu.RLock()
	current, identityErr := m.idleRecord(id, req)
	currentOK := identityErr == nil && current == record && record.state.State == "running" && record.process.PID == process.PID
	m.mu.RUnlock()
	switch {
	case !currentOK:
		proof.ExecutionState = "unknown"
	case errors.Is(err, ErrGatewayIdleBusy):
		proof.ExecutionState = "busy"
	case err != nil || release == nil:
		proof.ExecutionState = "unknown"
	default:
		proof.ExecutionState = "idle"
	}
	return proof, nil
}

// IdleStopGateway is graceful-only. persistFence is called after acquiring the
// supervisor admission guard and before cancellation/signaling. Its durable policy
// prevents supervisor restarts or another API from escalating this operation.
// An unconfirmed operation retains the guard and all writer/port reservations.
func (m *GatewayManager) IdleStopGateway(ctx context.Context, id string, req IdleStopRequest, persistFence func() error) error {
	m.mu.RLock()
	record, err := m.idleRecord(id, req)
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	record.stopMu.Lock()
	defer record.stopMu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	current, err := m.idleRecord(id, req)
	process, fenced, state := record.process, record.idleStop, record.state.State
	m.mu.RUnlock()
	if err != nil || current != record {
		return ErrGatewayStopConflict
	}
	if !fenced {
		if process.GracefulStop == nil {
			return ErrGatewayIdleUnsupported
		}
		if state != "running" {
			return ErrGatewayIdleUnknown
		}
		release, guardErr := m.acquireIdleGuard(ctx, record)
		if guardErr != nil {
			if release != nil {
				release()
			}
			return guardErr
		}
		if release == nil {
			return ErrGatewayIdleUnknown
		}
		m.mu.Lock()
		current, err = m.idleRecord(id, req)
		if err != nil || current != record || record.state.State != "running" || record.process.PID != process.PID || ctx.Err() != nil {
			m.mu.Unlock()
			release()
			return ErrGatewayStopConflict
		}
		persist := persistFence
		if process.FenceIdleStop != nil {
			persist = func() error { return process.FenceIdleStop(persistFence) }
		}
		if persist != nil {
			if err = persist(); err != nil {
				m.mu.Unlock()
				release()
				return err
			}
		}
		record.idleStop, record.idleRelease = true, release
		record.state.State = "stopping"
		record.state.UpdatedAt = time.Now().UTC()
		m.notifyGatewayStateChangedLocked()
		m.mu.Unlock()
	}

	record.cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-record.finished:
	}
	m.mu.RLock()
	reserved, process, cleanup, automationFailed := record.reserved, record.process, record.executionCleanup, record.automationStopFailed
	m.mu.RUnlock()
	if automationFailed {
		return ErrGatewayStopFailed
	}
	// The lifecycle owner has already made the first attempt. A failed attempt
	// is left for an explicit retry, never immediately escalated or retried here.
	if !fenced && reserved {
		return ErrGatewayStopFailed
	}
	if reserved {
		if process.GracefulStop == nil {
			return ErrGatewayStopFailed
		}
		if err = process.GracefulStop(ctx); err != nil {
			return err
		}
		if cleanup != nil {
			if err = cleanup(ctx); err != nil {
				return err
			}
		}
		m.mu.Lock()
		m.ports.Release(record.state.Port)
		record.reserved = false
		record.process = ManagedProcess{}
		record.executionCleanup = nil
		record.state.PID = 0
		record.state.State = "stopped"
		record.state.ErrorMessage = ""
		release := record.idleRelease
		record.idleRelease = nil
		m.notifyGatewayStateChangedLocked()
		m.mu.Unlock()
		if release != nil {
			release()
		}
	}
	return nil
}
