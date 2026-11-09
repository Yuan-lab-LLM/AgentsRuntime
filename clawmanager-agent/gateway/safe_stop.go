package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime"
	"time"
)

var (
	ErrGatewayStopConflict    = errors.New("gateway stop identity/state conflict; no replacement permitted")
	ErrGatewayStopUnsupported = errors.New("confirmed graceful stop is unavailable; legacy runtime remains unchanged")
)

type StopCapabilities struct {
	Protocol          int    `json:"protocol"`
	PodUID            string `json:"pod_uid"`
	AgentEpoch        string `json:"agent_epoch"`
	GracefulStop      bool   `json:"graceful_stop"`
	ConfirmationScope string `json:"confirmation_scope"`
	AutomaticUpgrade  bool   `json:"automatic_upgrade"`
	ExpectedPIDGuard  bool   `json:"expected_pid_guard"`
}

type ConfirmStopRequest struct {
	InstanceID     int    `json:"instance_id"`
	UserID         int    `json:"user_id"`
	Generation     *int   `json:"generation"`
	GatewayID      string `json:"gateway_id"`
	OperationID    string `json:"operation_id"`
	PodUID         string `json:"pod_uid"`
	AgentEpoch     string `json:"agent_epoch"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	ExpectedPID    int    `json:"expected_pid,omitempty"`
}

func (r ConfirmStopRequest) Valid() bool {
	b, err := hex.DecodeString(r.OperationID)
	return err == nil && len(b) == 16 && r.OperationID == hex.EncodeToString(b) &&
		r.InstanceID > 0 && r.UserID > 0 && r.Generation != nil && *r.Generation >= 0 &&
		r.GatewayID == gatewayID(r.InstanceID, *r.Generation) &&
		r.PodUID != "" && r.AgentEpoch != "" && r.TimeoutSeconds >= 1 && r.TimeoutSeconds <= 20 && r.ExpectedPID >= 0
}

type ConfirmStopResponse struct {
	InstanceID        int       `json:"instance_id"`
	Generation        int       `json:"generation"`
	GatewayID         string    `json:"gateway_id"`
	OperationID       string    `json:"operation_id"`
	PodUID            string    `json:"pod_uid"`
	AgentEpoch        string    `json:"agent_epoch"`
	PID               int       `json:"pid"`
	Confirmed         bool      `json:"confirmed"`
	ConfirmationScope string    `json:"confirmation_scope"`
	ConfirmedAt       time.Time `json:"confirmed_at"`
}

func newStopEpoch() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(value[:])
}

func (m *GatewayManager) StopCapabilities() StopCapabilities {
	return StopCapabilities{Protocol: 1, PodUID: m.cfg.PodUID, AgentEpoch: m.stopEpoch,
		GracefulStop:      runtime.GOOS == "linux" && !m.IsolatedGatewayLifecycle() && m.cfg.RuntimeType == "opencode" && m.cfg.PodUID != "" && m.stopEpoch != "",
		ConfirmationScope: "process_group", AutomaticUpgrade: false, ExpectedPIDGuard: true}
}

// ConfirmStop is an internal building block, not an upgrade coordinator. Its
// caller MUST hold a durable placement fence and have drained the application.
// The local fence is never auto-released on timeout or HTTP disconnect. A retry
// must carry the identical operation and agent epoch. No backup permission is
// implied: detached tools may have left the process group.
func (m *GatewayManager) ConfirmStop(ctx context.Context, req ConfirmStopRequest) (*ConfirmStopResponse, error) {
	if !req.Valid() {
		return nil, ErrGatewayStopConflict
	}
	// The process-group protocol belongs to the legacy execution path. Native
	// gateways retain their namespace, auxiliary-writer and credential cleanup
	// through the isolated lifecycle source-stop API.
	if m.IsolatedGatewayLifecycle() || m.cfg.RuntimeType != "opencode" || m.cfg.PodUID == "" || m.stopEpoch == "" {
		return nil, ErrGatewayStopUnsupported
	}
	if req.PodUID != m.cfg.PodUID || req.AgentEpoch != m.stopEpoch {
		return nil, ErrGatewayStopConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	record := m.gateways[req.GatewayID]
	if record == nil || record.state.InstanceID != req.InstanceID || record.state.UserID != req.UserID || record.state.Generation != *req.Generation ||
		record.stopBusy || record.deletePending || (record.stopOperation != "" && record.stopOperation != req.OperationID) {
		m.mu.Unlock()
		return nil, ErrGatewayStopConflict
	}
	if record.stopOperation == "" && record.state.State != "running" {
		m.mu.Unlock()
		return nil, ErrGatewayStopConflict
	}
	if record.process.GracefulStop == nil || record.process.PID <= 0 {
		m.mu.Unlock()
		return nil, ErrGatewayStopUnsupported
	}
	// Optional for older control clients. New upgrade coordinators require the
	// advertised capability and always pin PID before the first stop attempt.
	if req.ExpectedPID > 0 && req.ExpectedPID != record.process.PID {
		m.mu.Unlock()
		return nil, ErrGatewayStopConflict
	}
	record.stopOperation, record.stopBusy = req.OperationID, true
	record.state.State = "stopping"
	record.state.UpdatedAt = time.Now().UTC()
	process := record.process
	m.notifyGatewayStateChangedLocked()
	m.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	err := process.GracefulStop(stopCtx)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Fencing is checked again even though all current mutation paths reject it.
	if m.gateways[req.GatewayID] != record || record.stopOperation != req.OperationID {
		return nil, ErrGatewayStopConflict
	}
	record.stopBusy = false
	record.state.UpdatedAt = time.Now().UTC()
	if err != nil {
		record.stopConfirmed = false
		record.state.ErrorMessage = "graceful stop not confirmed; retry same operation; replacement blocked"
		m.notifyGatewayStateChangedLocked()
		return nil, err
	}
	record.stopConfirmed = true
	record.state.State = "stopped"
	record.state.ErrorMessage = ""
	m.notifyGatewayStateChangedLocked()
	return &ConfirmStopResponse{InstanceID: req.InstanceID, Generation: *req.Generation, GatewayID: req.GatewayID,
		OperationID: req.OperationID, PodUID: m.cfg.PodUID, AgentEpoch: m.stopEpoch, PID: process.PID,
		Confirmed: true, ConfirmationScope: "process_group", ConfirmedAt: record.state.UpdatedAt}, nil
}
