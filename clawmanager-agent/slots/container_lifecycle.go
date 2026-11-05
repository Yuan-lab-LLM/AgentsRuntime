package slots

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// This wire contract is also implemented by the trusted CM controller. The
// random operation and persisted worker epoch prevent stale stop-message reuse.
type containerStopIntent struct {
	Version          int    `json:"version"`
	OperationID      string `json:"operation_id"`
	PodUID           string `json:"pod_uid"`
	Slot             string `json:"slot"`
	GatewayID        string `json:"gateway_id"`
	InstanceID       int    `json:"instance_id"`
	UserID           int    `json:"user_id"`
	Generation       int    `json:"generation"`
	WorkerEpoch      string `json:"worker_epoch"`
	AssignmentDigest string `json:"assignment_digest"`
	Graceful         bool   `json:"graceful"`
	Kind             string `json:"kind,omitempty"`
}

type containerTerminationMessage struct {
	containerStopIntent
	CleanupConfirmed bool `json:"cleanup_confirmed"`
}

type containerTerminationProof struct {
	containerTerminationMessage
	ContainerID string    `json:"container_id"`
	FinishedAt  time.Time `json:"finished_at"`
}

func newExecutionToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func executionTokenValid(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16 && value == hex.EncodeToString(b)
}

func decodeContainerOperation(r *http.Request, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("invalid container operation")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing container operation")
	}
	return nil
}

func (w *Worker) matchesContainerIntent(intent containerStopIntent) bool {
	a := w.book.Assignment
	return a != nil && intent.Version == 1 && executionTokenValid(intent.OperationID) &&
		intent.PodUID == w.cfg.PodUID && intent.PodUID == a.PodUID && intent.Slot == w.slotName &&
		intent.GatewayID == a.GatewayID && intent.InstanceID == a.InstanceID && intent.UserID == a.UserID &&
		intent.Generation == a.Generation && intent.WorkerEpoch == a.WorkerEpoch && executionTokenValid(intent.WorkerEpoch) &&
		intent.AssignmentDigest == w.book.Digest && len(intent.AssignmentDigest) == 64
}

func (w *Worker) serveContainerStop(out http.ResponseWriter, r *http.Request, id string) {
	var intent containerStopIntent
	if err := decodeContainerOperation(r, &intent); err != nil || intent.GatewayID != id || intent.Kind != "" {
		http.Error(out, "invalid container stop intent", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.book.quarantined || !w.matchesContainerIntent(intent) || intent.WorkerEpoch != w.workerEpoch || w.book.Assignment.State == "stopped" {
		http.Error(out, "slot cannot prepare an unknown execution", 409)
		return
	}
	if w.book.StopIntent != nil && *w.book.StopIntent != intent {
		http.Error(out, "different container stop is already committed", 409)
		return
	}
	if w.book.StopIntent == nil {
		w.book.StopIntent = &intent
		if err := w.book.save(); err != nil {
			w.book.StopIntent = nil
			http.Error(out, "cannot persist container stop", 409)
			return
		}
	}
	if !w.resetReady {
		w.network.fenceForContainerReset()
		if err := w.manager.PrepareContainerReset(r.Context(), id, intent.Graceful, func() error {
			w.book.Assignment.IdleStop = true
			return w.book.save()
		}); err != nil {
			log.Printf("runtime-agent container reset preparation failed: %v", err)
			http.Error(out, "container reset preparation is incomplete", 409)
			return
		}
		if err := w.network.closeForContainerReset(r.Context()); err != nil {
			log.Printf("runtime-agent container network cleanup failed: %v", err)
			http.Error(out, "execution resource cleanup is incomplete", 409)
			return
		}
		message := containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}
		if err := writeContainerTermination(message); err != nil {
			http.Error(out, "cannot persist container cleanup receipt", 409)
			return
		}
		w.resetReady = true
	}
	writeJSON(out, http.StatusAccepted, map[string]any{"gateway_id": id, "operation_id": intent.OperationID, "pending": true, "stopped": false, "confirmed": false})
	if f, ok := out.(http.Flusher); ok {
		f.Flush()
	}
	select {
	case <-w.reset:
	default:
		close(w.reset)
	}
}

func (o *networkOwner) closeForContainerReset(ctx context.Context) error {
	o.mu.Lock()
	active := o.active
	o.mu.Unlock()
	if active == nil {
		return nil
	}
	return o.cleanup(active)(ctx)
}

func (o *networkOwner) fenceForContainerReset() {
	o.mu.Lock()
	active := o.active
	o.mu.Unlock()
	if active == nil {
		return
	}
	active.mu.Lock()
	active.closing = true
	fence := active.fenceIngress
	active.mu.Unlock()
	if fence != nil {
		fence()
	}
}

func writeContainerTermination(message containerTerminationMessage) error {
	path := os.Getenv("RUNTIME_SLOT_TERMINATION_MESSAGE_PATH")
	if path != "/dev/termination-log" {
		return errors.New("explicit slot termination message path is required")
	}
	commitment, err := containerTerminationCommitment(message)
	if err != nil {
		return err
	}
	data := []byte(commitment)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe termination message file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

// Keep below the kubelet's per-container share of the Pod termination-message
// budget even for a 256-slot pool. The full intent remains in the trusted book.
func containerTerminationCommitment(message containerTerminationMessage) (string, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "C1:" + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func (w *Worker) serveContainerReconcile(out http.ResponseWriter, r *http.Request) {
	var proof containerTerminationProof
	if err := decodeContainerOperation(r, &proof); err != nil {
		http.Error(out, "invalid container termination proof", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if proof.Kind != "" || !proof.CleanupConfirmed || proof.ContainerID == "" || proof.FinishedAt.IsZero() || proof.WorkerEpoch == w.workerEpoch || !w.matchesContainerIntent(proof.containerStopIntent) || w.book.StopIntent == nil || *w.book.StopIntent != proof.containerStopIntent {
		http.Error(out, "container proof does not match old execution", 409)
		return
	}
	if w.book.TerminationProof != nil && *w.book.TerminationProof != proof {
		http.Error(out, "container proof changed", 409)
		return
	}
	if w.book.Assignment.State != "stopped" {
		a := w.book.Assignment
		req := gateway.CreateGatewayRequest{InstanceID: a.InstanceID, UserID: a.UserID, Generation: a.Generation, PodUID: a.PodUID, AgentType: w.cfg.RuntimeType}
		lockPath, writerPath, err := w.writerPaths(req)
		if err != nil {
			http.Error(out, "old writer path is unavailable", 409)
			return
		}
		lease, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			http.Error(out, "old writer lock is unavailable", 409)
			return
		}
		defer lease.Close()
		if err = lockWorkspace(lease); err != nil {
			http.Error(out, "old writer is still locked", 409)
			return
		}
		if err = stopWriter(writerPath, req, w.slotName); err != nil {
			http.Error(out, "old writer cannot be reconciled", 409)
			return
		}
		previous, quarantined := a.State, w.book.quarantined
		a.State = "stopped"
		// The controller must durably confirm stopped before permitting slot
		// reuse, otherwise a lost final receipt could be overwritten by create.
		w.book.quarantined = true
		w.book.TerminationProof = &proof
		if err = w.book.save(); err != nil {
			a.State = previous
			w.book.quarantined = quarantined
			w.book.TerminationProof = nil
			http.Error(out, "old assignment stop cannot be persisted", 409)
			return
		}
		if err = lease.Close(); err != nil {
			w.book.quarantined = true
			http.Error(out, "old writer release is uncertain", 409)
			return
		}
	}
	writeJSON(out, 200, map[string]any{"gateway_id": proof.GatewayID, "operation_id": proof.OperationID, "container_id": proof.ContainerID, "worker_epoch": proof.WorkerEpoch, "stopped": true, "confirmed": true, "unmounted": true, "graceful": proof.Graceful})
}

func (w *Worker) serveContainerRelease(out http.ResponseWriter, r *http.Request) {
	var proof containerTerminationProof
	if decodeContainerOperation(r, &proof) != nil || !proof.CleanupConfirmed || proof.ContainerID == "" || proof.FinishedAt.IsZero() || proof.Version != 1 || proof.InstanceID < 1 || proof.UserID < 1 || proof.Generation < 1 || proof.GatewayID != fmt.Sprintf("gw-%d-%d", proof.InstanceID, proof.Generation) || len(proof.AssignmentDigest) != 64 || !executionTokenValid(proof.OperationID) || !executionTokenValid(proof.WorkerEpoch) || proof.PodUID != w.cfg.PodUID || proof.Slot != w.slotName {
		http.Error(out, "invalid finalized stop proof", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.book.Assignment
	if a == nil {
		http.Error(out, "slot release state is unknown", 409)
		return
	}
	superseded := a.GatewayID != proof.GatewayID || a.InstanceID != proof.InstanceID || a.Generation != proof.Generation || a.WorkerEpoch != proof.WorkerEpoch
	if !superseded {
		if w.book.Maintenance != nil && w.book.Maintenance.Uncertain {
			http.Error(out, "native storage uncertainty still fences this slot", 409)
			return
		}
		if a.State != "stopped" || w.book.TerminationProof == nil || *w.book.TerminationProof != proof {
			http.Error(out, "slot release proof changed", 409)
			return
		}
		previous := w.book.StopFinalized
		w.book.StopFinalized = true
		if err := w.book.save(); err != nil {
			w.book.StopFinalized = previous
			http.Error(out, "cannot persist slot release", 409)
			return
		}
		w.book.quarantined = false
	}
	// A later assignment could only have been admitted after this quarantine
	// was finalized. A retry acknowledges availability only; it never changes
	// or releases the current assignment's writer.
	writeJSON(out, 200, map[string]any{"gateway_id": proof.GatewayID, "operation_id": proof.OperationID, "container_id": proof.ContainerID, "worker_epoch": proof.WorkerEpoch, "released": true, "superseded": superseded})
}

func (w *Worker) containerStopFenced() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.book.StopIntent != nil && w.book.Assignment != nil && w.book.Assignment.State != "stopped"
}
