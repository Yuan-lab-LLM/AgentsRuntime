package slots

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// Pending cancellation is scoped to the original slot, never inferred from an
// unknown DELETE. Its receipt fences late creates via the shared writer record.
type pendingCancellation struct {
	InstanceID  int    `json:"instance_id"`
	UserID      int    `json:"user_id"`
	Generation  int    `json:"generation"`
	PodUID      string `json:"pod_uid"`
	AgentType   string `json:"agent_type"`
	GatewayPort int    `json:"gateway_port"`
}

func decodePendingCancellation(data []byte, cfg gateway.Config) (pendingCancellation, error) {
	var req pendingCancellation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > 4096 || decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.PodUID == "" || req.PodUID != cfg.PodUID || req.AgentType == "" || req.AgentType != cfg.RuntimeType || req.GatewayPort < 1 {
		return req, errors.New("invalid cancellation scope")
	}
	return req, nil
}

// The caller holds worker.mu, excluding create/stop admission. Active writers
// still require the existing confirmed-stop path; cancellation never stops one.
func (w *Worker) cancelPending(scope pendingCancellation) error {
	if w.book.quarantined || w.network.unavailable() || w.manager.Health() != nil {
		return errors.New("writer state unavailable")
	}
	if a := w.book.Assignment; a != nil && a.InstanceID == scope.InstanceID && (a.UserID != scope.UserID || a.State != "stopped") {
		return errors.New("active or mismatched assignment")
	}
	for _, state := range w.manager.GatewayStates() {
		if state.InstanceID == scope.InstanceID {
			return errors.New("gateway still owned")
		}
	}
	req := gateway.CreateGatewayRequest{InstanceID: scope.InstanceID, UserID: scope.UserID, Generation: scope.Generation, PodUID: scope.PodUID, AgentType: scope.AgentType, GatewayPort: scope.GatewayPort}
	lockPath, recordPath, err := w.writerPaths(req)
	if err != nil {
		return err
	}
	lease, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = lockWorkspace(lease); err != nil {
		return err
	}
	// This lock is independent of w.lease: cancelling an unpublished generation
	// must not replace another instance's current writer bookkeeping.
	if err = cancelPendingWriter(recordPath, req, w.slotName); err != nil {
		return err
	}
	return lease.Close()
}

func cancelPendingWriter(path string, req gateway.CreateGatewayRequest, slot string) error {
	previous, err := readWriter(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous != nil {
		if previous.InstanceID != req.InstanceID || previous.UserID != req.UserID || previous.RuntimeType != req.AgentType || previous.State != "stopped" {
			return fmt.Errorf("previous writer is not confirmed stopped")
		}
		if previous.Generation >= req.Generation {
			// Re-sync even a retry: an earlier rename may have succeeded while
			// its directory sync failed and the caller received no receipt.
			return atomicJSON(path, previous)
		}
	}
	return atomicJSON(path, writerRecord{InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, RuntimeType: req.AgentType, PodUID: req.PodUID, Slot: slot, State: "stopped"})
}
