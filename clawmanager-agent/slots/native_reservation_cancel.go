package slots

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

// Cancellation is supported only before the native coordinator records or
// dispatches compatibility work. A tombstone is a fence, not an exit receipt.
type nativeReservationIdentity struct {
	OperationID string `json:"operation_id"`
	PodUID      string `json:"pod_uid"`
	Action      string `json:"action"`
	InstanceID  int    `json:"instance_id"`
	UserID      int    `json:"user_id"`
	Generation  int    `json:"generation"`
}

type nativeReservationCancellation struct {
	Version int `json:"version"`
	nativeReservationIdentity
}

func (p *Pool) cancelUnstartedNativeReservation(ctx context.Context, id nativeReservationIdentity) (nativeReservationCancellation, error) {
	var empty nativeReservationCancellation
	if !executionTokenValid(id.OperationID) || id.PodUID == "" || id.PodUID != p.cfg.PodUID || id.Action != "compatibility" || id.InstanceID < 1 || id.UserID < 1 || id.Generation < 1 {
		return empty, fmt.Errorf("native reservation identity changed")
	}
	p.proxyMu.Lock()
	defer p.proxyMu.Unlock()
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	if err := workspaceio.PrepareNFSManagementRoots(p.cfg.WorkspaceRoot); err != nil {
		return empty, err
	}
	path, err := p.nativeRecordPath(id.OperationID)
	if err != nil {
		return empty, err
	}
	unlock, err := p.metadata.LockUpgradeOperation(id.InstanceID)
	if err != nil {
		return empty, err
	}
	defer unlock()
	if p.maintenance[id.OperationID] != nil {
		return empty, fmt.Errorf("native operation already has an active coordinator")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return empty, fmt.Errorf("native operation record exists or cannot be inspected")
	}
	cancelPath := path + ".cancelled"
	if err := trustedFile(cancelPath); err != nil {
		return empty, err
	}
	receipt := nativeReservationCancellation{Version: 1, nativeReservationIdentity: id}
	data, err := os.ReadFile(cancelPath)
	if err == nil {
		var old nativeReservationCancellation
		if len(data) > 4096 || json.Unmarshal(data, &old) != nil || old != receipt {
			return empty, fmt.Errorf("native reservation cancellation identity changed")
		}
		return old, nil
	}
	if !os.IsNotExist(err) {
		return empty, err
	}
	snapshots, healthy := p.snapshots(ctx)
	if len(snapshots) == 0 || healthy != len(snapshots) || ctx.Err() != nil {
		return empty, fmt.Errorf("native slots cannot be verified before cancellation")
	}
	for _, snapshot := range snapshots {
		if snapshot.MaintenanceOperationID == id.OperationID {
			return empty, fmt.Errorf("native operation is present in a worker slot")
		}
	}
	if err := atomicJSON(cancelPath, receipt); err != nil {
		return empty, err
	}
	return receipt, nil
}

func (p *Pool) serveCancelUnstartedNativeReservation(out http.ResponseWriter, r *http.Request) {
	var id nativeReservationIdentity
	if decodeNativeWorkerRequest(r, &id) != nil {
		http.Error(out, "invalid native reservation cancellation", http.StatusBadRequest)
		return
	}
	receipt, err := p.cancelUnstartedNativeReservation(r.Context(), id)
	if err != nil {
		http.Error(out, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(out, http.StatusOK, receipt)
}
