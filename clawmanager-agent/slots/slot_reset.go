package slots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// These fields and their order are the CM runtimeeviction.SlotScope digest contract.
type slotResetScope struct {
	Version       int    `json:"version"`
	OperationID   string `json:"operation_id"`
	Namespace     string `json:"namespace"`
	PodName       string `json:"pod_name"`
	PodUID        string `json:"pod_uid"`
	NodeName      string `json:"node_name"`
	NodeUID       string `json:"node_uid"`
	NodeBootID    string `json:"node_boot_id"`
	ContainerName string `json:"container_name"`
	ContainerID   string `json:"container_id"`
}

type slotResetReceipt struct {
	ScopeDigest   string    `json:"scope_digest"`
	OperationID   string    `json:"operation_id"`
	PodUID        string    `json:"pod_uid"`
	ContainerName string    `json:"container_name"`
	ContainerID   string    `json:"container_id"`
	NodeUID       string    `json:"node_uid"`
	NodeBootID    string    `json:"node_boot_id"`
	FinishedAt    time.Time `json:"finished_at"`
	Confirmed     bool      `json:"confirmed"`
}

// CurrentWorkerEpoch addresses the live worker. All remaining fields are
// immutable through retries, including across restarts of that worker.
type slotResetProof struct {
	Scope            slotResetScope   `json:"scope"`
	Receipt          slotResetReceipt `json:"receipt"`
	WorkerEpoch      string           `json:"worker_epoch"`
	AssignmentDigest string           `json:"assignment_digest"`
}

type slotResetRequest struct {
	slotResetProof
	CurrentWorkerEpoch string `json:"current_worker_epoch"`
}

func resetDigestValid(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && value == hex.EncodeToString(b)
}

func (w *Worker) validateSlotReset(p slotResetRequest) error {
	s, r := p.Scope, p.Receipt
	if p.CurrentWorkerEpoch != w.workerEpoch || !executionTokenValid(w.workerEpoch) || p.WorkerEpoch == w.workerEpoch || p.WorkerEpoch != "" && !executionTokenValid(p.WorkerEpoch) {
		return errors.New("physical slot reset worker epoch changed")
	}
	if s.Version != 1 || s.PodUID != w.cfg.PodUID || s.Namespace != w.cfg.Namespace || s.PodName != w.cfg.PodName || w.cfg.NodeName != "" && s.NodeName != w.cfg.NodeName || s.ContainerName != w.slotName || !strings.HasPrefix(s.ContainerID, "containerd://") || !resetDigestValid(strings.TrimPrefix(s.ContainerID, "containerd://")) {
		return errors.New("physical slot scope changed")
	}
	for _, v := range []string{s.OperationID, s.Namespace, s.PodName, s.PodUID, s.NodeName, s.NodeUID, s.NodeBootID} {
		if v == "" || len(v) > 256 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("incomplete physical slot identity")
		}
	}
	raw, _ := json.Marshal(s)
	if !r.Confirmed || r.ScopeDigest != recoveryHash(raw) || r.OperationID != s.OperationID || r.PodUID != s.PodUID || r.ContainerName != s.ContainerName || r.ContainerID != s.ContainerID || r.NodeUID != s.NodeUID || r.NodeBootID != s.NodeBootID || r.FinishedAt.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || r.FinishedAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("exact old container termination receipt changed")
	}
	return nil
}

func (b *book) matchesSlotReset(p slotResetProof) bool {
	a := b.Assignment
	if a == nil {
		return !b.unrecognizedEmpty && p.AssignmentDigest == "" && b.Digest == "" && b.StopIntent == nil && b.TerminationProof == nil && !b.StopFinalized && b.FaultRecovery == nil && b.LastFaultRelease == nil
	}
	return a.InstanceID > 0 && a.UserID > 0 && a.Generation > 0 && a.GatewayID == fmt.Sprintf("gw-%d-%d", a.InstanceID, a.Generation) && a.PodUID == p.Scope.PodUID && (a.State == "starting" || a.State == "stopped" || a.State == "running") && executionTokenValid(p.WorkerEpoch) && a.WorkerEpoch == p.WorkerEpoch && resetDigestValid(p.AssignmentDigest) && b.Digest == p.AssignmentDigest
}

// The worker's lifetime slot guard excludes other writers in this directory.
// One receipt per operation survives later assignments and reconciliations.
func readSlotResetRelease(path string, p slotResetProof) (bool, error) {
	if err := trustedDirectory(filepath.Dir(path)); err != nil {
		return false, err
	}
	f, info, err := openSourceProofFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return false, err
	}
	var saved slotResetProof
	if decodeFaultRecovery(raw, &saved) != nil || saved != p {
		return false, errors.New("immutable slot reset receipt conflicts")
	}
	return true, sourceProofFileUnchanged(path, f, info)
}

func (w *Worker) serveSlotReset(out http.ResponseWriter, r *http.Request, release bool) {
	var req slotResetRequest
	if decodeContainerOperation(r, &req) != nil {
		http.Error(out, "invalid physical slot reset request", 400)
		return
	}
	req.Receipt.FinishedAt = req.Receipt.FinishedAt.UTC()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.validateSlotReset(req); err != nil {
		http.Error(out, err.Error(), 409)
		return
	}
	if err := w.reconcileSlotReset(req.slotResetProof, release); err != nil {
		http.Error(out, err.Error(), 409)
		return
	}
	writeJSON(out, 200, map[string]any{"operation_id": req.Scope.OperationID, "scope_digest": req.Receipt.ScopeDigest, "container_id": req.Scope.ContainerID, "worker_epoch": w.workerEpoch, "stopped": true, "confirmed": true, "released": release})
}

func (w *Worker) reconcileSlotReset(p slotResetProof, release bool) error {
	b := w.book
	path := filepath.Join(filepath.Dir(b.path), "reset-release-"+recoveryHash([]byte(p.Scope.OperationID))+".json")
	saved, err := readSlotResetRelease(path, p)
	if err != nil {
		return fmt.Errorf("read immutable slot reset receipt: %w", err)
	}

	if saved && (b.PendingReset == nil || *b.PendingReset != p) {
		if release {
			return nil
		} // Historical acknowledgement only.
		if b.PendingReset == nil && b.matchesSlotReset(p) && w.idleConfirmed() {
			return nil
		}
		return errors.New("slot has a subsequent owner or reset")
	}
	if b.PendingReset != nil && *b.PendingReset != p {
		return errors.New("different physical slot reset already committed")
	}
	if !b.matchesSlotReset(p) {
		return errors.New("old slot book ownership cannot be verified")
	}
	if !w.faultRecoveryLocallyIdle() {
		return errors.New("slot has a live execution, lease or native maintenance")
	}
	if b.FaultRecovery != nil && !b.faultFinalized() || b.TerminationProof != nil && !b.StopFinalized {
		return errors.New("old slot has an unfinished termination or fault operation")
	}
	if !release {
		if b.PendingReset != nil {
			return nil
		}
		next := *b
		next.PendingReset = &p
		next.quarantined = true
		if err := next.save(); err != nil {
			return fmt.Errorf("persist pending physical reset: %w", err)
		}
		*b = next
		return nil
	}
	if b.PendingReset == nil {
		return errors.New("physical slot reset has not been reconciled")
	}
	if !saved {
		if err := atomicJSON(path, p); err != nil {
			return fmt.Errorf("persist immutable slot reset receipt: %w", err)
		}
	}
	next := *b
	next.PendingReset = nil
	next.quarantined = false
	if a := b.Assignment; a != nil {
		old := *a
		old.State = "stopped"
		// Supersede an uncompleted ordinary intent without granting C1 or
		// graceful completion to a delayed ordinary reconciliation.
		if b.TerminationProof == nil && b.FaultRecovery == nil {
			next.StopIntent = nil
			next.AuxiliaryExitCommitment = ""
			old.IdleStop = false
		}
		next.Assignment = &old
	}
	if err := next.save(); err != nil {
		return fmt.Errorf("persist physical slot reset release: %w", err)
	}
	*b = next
	return nil
}
