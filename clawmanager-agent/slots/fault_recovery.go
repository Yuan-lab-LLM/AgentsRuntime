package slots

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Independent node recovery is a different authority from a graceful container
// termination. Keep its receipt separate; never synthesize TerminationProof.
type faultSlotRecovery struct {
	CurrentWorkerEpoch string          `json:"current_worker_epoch"`
	Scope              json.RawMessage `json:"scope"`
	Receipt            json.RawMessage `json:"receipt"`
	WorkerEpoch        string          `json:"worker_epoch"`
	AssignmentDigest   string          `json:"assignment_digest"`
}
type faultRecoveryScope struct {
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
	RuntimeType   string `json:"runtime_type"`
	GatewayID     string `json:"gateway_id"`
	WorkspaceRoot string `json:"workspace_root"`
	InstanceID    int    `json:"instance_id"`
	UserID        int    `json:"user_id"`
	Generation    int    `json:"generation"`
}
type faultRecoveryReceipt struct {
	ScopeDigest         string    `json:"scope_digest"`
	OperationID         string    `json:"operation_id"`
	PodUID              string    `json:"pod_uid"`
	ContainerName       string    `json:"container_name"`
	NodeUID             string    `json:"node_uid"`
	NodeBootID          string    `json:"node_boot_id"`
	StoppedContainerIDs []string  `json:"stopped_container_ids"`
	LedgerDigest        string    `json:"ledger_digest"`
	FinishedAt          time.Time `json:"finished_at"`
	Confirmed           bool      `json:"confirmed"`
}

func decodeFaultRecovery(data []byte, out any) error {
	if len(data) == 0 || len(data) > 16384 {
		return errors.New("invalid fault recovery size")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing fault recovery")
	}
	return nil
}
func compactRecoveryJSON(raw []byte) []byte {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return nil
	}
	return b.Bytes()
}
func recoveryHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// The receipt is an existing supervisor-owned, fsynced record written by the
// independent node helper after CRI stop + writer cleanup. Read it without
// opening or changing the business writer: that writer may already be newer.
func (w *Worker) validateFaultRecovery(p faultSlotRecovery) (faultRecoveryScope, faultRecoveryReceipt, error) {
	var s faultRecoveryScope
	var r faultRecoveryReceipt
	if decodeFaultRecovery(p.Scope, &s) != nil || decodeFaultRecovery(p.Receipt, &r) != nil {
		return s, r, errors.New("invalid fault recovery proof")
	}
	a := w.book.Assignment
	digest, err := hex.DecodeString(p.AssignmentDigest)
	if a == nil || s.Version != 1 || s.OperationID == "" || s.Namespace == "" || s.PodName == "" || s.NodeName == "" || s.NodeUID == "" || s.NodeBootID == "" || s.PodUID != w.cfg.PodUID || s.PodUID != a.PodUID || s.ContainerName != w.slotName || s.RuntimeType != w.cfg.RuntimeType || s.WorkspaceRoot != w.cfg.WorkspaceRoot || s.InstanceID != a.InstanceID || s.UserID != a.UserID || s.Generation != a.Generation || s.GatewayID != a.GatewayID || s.GatewayID != fmt.Sprintf("gw-%d-%d", s.InstanceID, s.Generation) || s.ContainerID == "" || !executionTokenValid(p.WorkerEpoch) || p.WorkerEpoch != a.WorkerEpoch || p.WorkerEpoch == w.workerEpoch || p.CurrentWorkerEpoch != w.workerEpoch || !executionTokenValid(w.workerEpoch) || p.AssignmentDigest != w.book.Digest || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != p.AssignmentDigest {
		return s, r, errors.New("fault proof does not match old execution")
	}
	if !r.Confirmed || r.ScopeDigest != recoveryHash(compactRecoveryJSON(p.Scope)) || r.OperationID != s.OperationID || r.PodUID != s.PodUID || r.ContainerName != s.ContainerName || r.NodeUID != s.NodeUID || r.NodeBootID != s.NodeBootID || len(r.StoppedContainerIDs) != 1 || r.StoppedContainerIDs[0] != s.ContainerID || r.FinishedAt.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || r.FinishedAt.After(time.Now().Add(5*time.Minute)) {
		return s, r, errors.New("independent stop receipt identity changed")
	}
	ledger, err := hex.DecodeString(r.LedgerDigest)
	if err != nil || len(ledger) != 32 || hex.EncodeToString(ledger) != r.LedgerDigest {
		return s, r, errors.New("invalid fault ledger digest")
	}
	dir := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	for _, path := range []string{w.cfg.WorkspaceRoot, dir} {
		if err = trustedDirectory(path); err != nil {
			return s, r, err
		}
	}
	path := filepath.Join(dir, ".recovery-"+r.ScopeDigest+".json")
	file, info, err := openSourceProofFile(path, os.O_RDONLY)
	if err != nil {
		return s, r, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return s, r, err
	}
	var stored faultRecoveryReceipt
	if decodeFaultRecovery(raw, &stored) != nil {
		return s, r, errors.New("invalid durable recovery receipt")
	}
	expected, _ := json.Marshal(r)
	actual, _ := json.Marshal(stored)
	if !bytes.Equal(expected, actual) {
		return s, r, errors.New("durable recovery receipt changed")
	}
	if err = sourceProofFileUnchanged(path, file, info); err != nil {
		return s, r, err
	}
	return s, r, nil
}
func sameFaultRecovery(a, b *faultSlotRecovery) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.WorkerEpoch == b.WorkerEpoch && a.AssignmentDigest == b.AssignmentDigest && bytes.Equal(compactRecoveryJSON(a.Scope), compactRecoveryJSON(b.Scope)) && bytes.Equal(compactRecoveryJSON(a.Receipt), compactRecoveryJSON(b.Receipt))
}
func (w *Worker) faultRecoveryLocallyIdle() bool {
	if w.lease != nil || w.containerPreparation != nil || w.resetReady || w.book.Maintenance != nil || w.manager.Health() != nil || !w.manager.ExecutionIdle() {
		return false
	}
	if w.network != nil {
		w.network.mu.Lock()
		active := w.network.active != nil
		w.network.mu.Unlock()
		if active {
			return false
		}
	}
	return true
}
func (w *Worker) serveFaultRecovery(out http.ResponseWriter, r *http.Request, release bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16385))
	var proof faultSlotRecovery
	if err != nil || decodeFaultRecovery(raw, &proof) != nil {
		http.Error(out, "invalid fault recovery", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if release && w.savedFaultRelease(proof) && proof.CurrentWorkerEpoch == w.workerEpoch && (w.book.faultFinalized() || w.book.Assignment == nil || w.book.Assignment.WorkerEpoch != proof.WorkerEpoch || w.book.Digest != proof.AssignmentDigest) {
		var s faultRecoveryScope
		var receipt faultRecoveryReceipt
		if decodeFaultRecovery(proof.Scope, &s) != nil || decodeFaultRecovery(proof.Receipt, &receipt) != nil || s.PodUID != w.cfg.PodUID || s.ContainerName != w.slotName || s.RuntimeType != w.cfg.RuntimeType {
			http.Error(out, "finalized fault identity changed", 409)
			return
		}
		writeJSON(out, 200, map[string]any{"operation_id": s.OperationID, "scope_digest": receipt.ScopeDigest, "gateway_id": s.GatewayID, "worker_epoch": w.workerEpoch, "stopped": true, "confirmed": true, "released": true})
		return
	}
	scope, receipt, err := w.validateFaultRecovery(proof)
	if err != nil || !w.faultRecoveryLocallyIdle() {
		http.Error(out, "fault recovery proof or local execution is not confirmed", 409)
		return
	}
	if w.book.FaultRecovery != nil && !sameFaultRecovery(w.book.FaultRecovery, &proof) {
		http.Error(out, "different fault recovery is already committed", 409)
		return
	}
	a := w.book.Assignment
	if release {
		if a.State != "stopped" || !sameFaultRecovery(w.book.FaultRecovery, &proof) {
			http.Error(out, "fault recovery has not been reconciled", 409)
			return
		}
		// Persist an immutable release receipt before updating the book. A crash
		// here leaves the old book quarantined; only the subsequent book commit
		// grants reuse. Separate receipts survive any number of later faults.
		path := w.faultReleasePath(proof)
		if !w.savedFaultRelease(proof) {
			if _, err = os.Lstat(path); !os.IsNotExist(err) {
				http.Error(out, "fault release record conflicts", 409)
				return
			}
			if err = trustedDirectory(filepath.Dir(path)); err != nil {
				http.Error(out, "fault release directory unavailable", 409)
				return
			}
			if err = atomicJSON(path, proof); err != nil {
				http.Error(out, "cannot persist immutable fault release", 409)
				return
			}
		}
		previous := w.book.LastFaultRelease
		w.book.LastFaultRelease = &proof
		if err = w.book.save(); err != nil {
			w.book.LastFaultRelease = previous
			http.Error(out, "cannot persist fault slot release", 409)
			return
		}
		w.book.quarantined = false
	} else {
		previous, state, quarantined := w.book.FaultRecovery, a.State, w.book.quarantined
		w.book.FaultRecovery = &proof
		a.State = "stopped"
		w.book.quarantined = !w.book.StopFinalized && !w.book.faultFinalized()
		if err = w.book.save(); err != nil {
			w.book.FaultRecovery = previous
			a.State = state
			w.book.quarantined = quarantined
			http.Error(out, "cannot persist fault slot reconciliation", 409)
			return
		}
	}
	writeJSON(out, 200, map[string]any{"operation_id": scope.OperationID, "scope_digest": receipt.ScopeDigest, "gateway_id": scope.GatewayID, "worker_epoch": w.workerEpoch, "stopped": true, "confirmed": true, "released": release})
}

func (w *Worker) faultReleasePath(p faultSlotRecovery) string {
	return filepath.Join(filepath.Dir(w.book.path), "fault-release-"+recoveryHash(compactRecoveryJSON(p.Scope))+".json")
}
func (w *Worker) savedFaultRelease(p faultSlotRecovery) bool {
	path := w.faultReleasePath(p)
	if trustedDirectory(filepath.Dir(path)) != nil {
		return false
	}
	file, info, err := openSourceProofFile(path, os.O_RDONLY)
	if err != nil {
		return false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	var saved faultSlotRecovery
	return err == nil && decodeFaultRecovery(raw, &saved) == nil && sameFaultRecovery(&saved, &p) && sourceProofFileUnchanged(path, file, info) == nil
}
