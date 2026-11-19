package slots

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// Management-only emptyDir state survives the maintenance container's reset.
// Parent coordinator fences remain outside that container until exact teardown.
type nativeMaintenanceBook struct {
	OperationID string              `json:"operation_id"`
	Operation   json.RawMessage     `json:"operation"`
	Intent      containerStopIntent `json:"intent"`
	Result      json.RawMessage     `json:"result,omitempty"`
	Error       string              `json:"error,omitempty"`
	Ready       bool                `json:"ready"`
	Started     bool                `json:"started"`
	Uncertain   bool                `json:"uncertain,omitempty"`
}

type nativeUpgradeWorkerRequest struct {
	Operation gateway.NativeUpgradeOperation `json:"operation"`
}

func decodeNativeWorkerRequest(r *http.Request, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return fmt.Errorf("invalid native maintenance request")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing native maintenance request")
	}
	return nil
}

func (w *Worker) serveNativeMaintenance(out http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		out.WriteHeader(405)
		return
	}
	var request nativeUpgradeWorkerRequest
	if decodeNativeWorkerRequest(r, &request) != nil || request.Operation.Validate() != nil {
		http.Error(out, "invalid native maintenance operation", 400)
		return
	}
	op := request.Operation
	operation, err := json.Marshal(op)
	if err != nil {
		http.Error(out, "invalid native maintenance operation", 400)
		return
	}
	w.mu.Lock()
	intent, err := w.reserveNativeMaintenance(op.OperationID, operation, op.Request.InstanceID, op.Request.UserID, op.Request.Generation)
	if err != nil {
		w.mu.Unlock()
		http.Error(out, "native maintenance requires the reserved idle slot", 409)
		return
	}
	m := w.book.Maintenance
	start := !m.Started && intent.WorkerEpoch == w.workerEpoch
	if start {
		m.Started = true
		if err = w.book.save(); err != nil {
			m.Started = false
			w.mu.Unlock()
			http.Error(out, "native maintenance reservation is not durable", 409)
			return
		}
	}
	ready := m.Ready && intent.WorkerEpoch == w.workerEpoch
	w.mu.Unlock()
	writeJSON(out, 202, nativeMaintenancePending{Pending: true, Intent: intent})
	if f, ok := out.(http.Flusher); ok {
		f.Flush()
	}
	if start {
		go w.runNativeMaintenance(op)
	} else if ready {
		go w.finishNativeMaintenanceReset()
	}
}

func (w *Worker) runNativeMaintenance(op gateway.NativeUpgradeOperation) {
	// The reservation survives an HTTP disconnect. Parent verification and the
	// container boundary, rather than a request deadline, own this execution.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	result, runErr := ExecuteNativeUpgrade(ctx, w.cfg, op, func(ctx context.Context) error { return VerifyNativeUpgradeParent(ctx, op.OperationID) })
	w.mu.Lock()
	m := w.book.Maintenance
	if m == nil || m.OperationID != op.OperationID || m.Intent.WorkerEpoch != w.workerEpoch {
		w.mu.Unlock()
		return
	}
	if len(result) > 1<<20 || len(result) > 0 && !json.Valid(result) {
		runErr = fmt.Errorf("native maintenance result is invalid")
		result = nil
	}
	m.Result = bytes.Clone(result)
	m.Ready = true
	m.Uncertain = errors.Is(runErr, gateway.ErrNativeStorageUncertain)
	if runErr != nil {
		m.Error = runErr.Error()
		if len(m.Error) > 4096 {
			m.Error = m.Error[:4096]
		}
	}
	if err := w.book.save(); err != nil {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.finishNativeMaintenanceReset()
}

func (w *Worker) finishNativeMaintenanceReset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := w.book.Maintenance
	if m == nil || !m.Ready || m.Intent.WorkerEpoch != w.workerEpoch || w.resetReady {
		return
	}
	if err := writeContainerTermination(containerTerminationMessage{containerStopIntent: m.Intent, CleanupConfirmed: true}); err != nil {
		return
	}
	w.resetReady = true
	select {
	case <-w.reset:
	default:
		close(w.reset)
	}
}

func (w *Worker) serveNativeMaintenanceReconcile(out http.ResponseWriter, r *http.Request) {
	var proof containerTerminationProof
	if r.Method != http.MethodPost || decodeContainerOperation(r, &proof) != nil || !validContainerProofSource(proof) {
		http.Error(out, "invalid maintenance proof", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	m := w.book.Maintenance
	if m == nil || !m.Ready || proof.Kind != "maintenance" || !proof.CleanupConfirmed || proof.ContainerID == "" || proof.FinishedAt.IsZero() || proof.WorkerEpoch == w.workerEpoch || m.Intent != proof.containerStopIntent || !w.matchesContainerIntent(proof.containerStopIntent) || w.book.StopIntent == nil || *w.book.StopIntent != proof.containerStopIntent {
		http.Error(out, "maintenance result lacks matching old container termination", 409)
		return
	}
	if w.book.TerminationProof != nil && *w.book.TerminationProof != proof {
		http.Error(out, "maintenance termination proof changed", 409)
		return
	}
	previousState := w.book.Assignment.State
	previousProof := w.book.TerminationProof
	w.book.Assignment.State = "stopped"
	w.book.TerminationProof = &proof
	w.book.quarantined = true
	if err := w.book.save(); err != nil {
		w.book.Assignment.State = previousState
		w.book.TerminationProof = previousProof
		http.Error(out, "maintenance result cannot be durably reconciled", 409)
		return
	}
	writeJSON(out, 200, nativeMaintenanceResult{Intent: m.Intent, Result: m.Result, Error: m.Error, Confirmed: true, Uncertain: m.Uncertain})
}

type nativeMaintenancePending struct {
	Pending bool                `json:"pending"`
	Intent  containerStopIntent `json:"intent"`
}

type nativeMaintenanceResult struct {
	Intent    containerStopIntent `json:"intent"`
	Result    json.RawMessage     `json:"result,omitempty"`
	Error     string              `json:"error,omitempty"`
	Confirmed bool                `json:"confirmed"`
	Uncertain bool                `json:"uncertain,omitempty"`
}

func canonicalNativeOperation(operation json.RawMessage) (json.RawMessage, error) {
	if len(operation) == 0 || len(operation) > 1<<20 {
		return nil, fmt.Errorf("native operation payload is invalid")
	}
	d := json.NewDecoder(bytes.NewReader(operation))
	d.UseNumber()
	var value map[string]any
	if err := d.Decode(&value); err != nil || value == nil {
		return nil, fmt.Errorf("native operation must be an object")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing native operation")
	}
	return json.Marshal(value)
}

// Caller holds worker.mu. Reservation is durable before any native CLI can run.
// A restarted worker returns the old intent but must never relaunch its helper.
func (w *Worker) reserveNativeMaintenance(operationID string, operation json.RawMessage, instanceID, userID, generation int) (containerStopIntent, error) {
	var empty containerStopIntent
	if w.cfg.SandboxProfile != "nfs-rootless-v1" || !executionTokenValid(operationID) || !executionTokenValid(w.workerEpoch) || instanceID < 1 || userID < 1 || generation < 1 {
		return empty, fmt.Errorf("invalid native maintenance identity")
	}
	canonical, err := canonicalNativeOperation(operation)
	if err != nil {
		return empty, err
	}
	if previous := w.book.Maintenance; previous != nil && previous.OperationID == operationID {
		if !bytes.Equal(previous.Operation, canonical) || previous.Intent.InstanceID != instanceID || previous.Intent.UserID != userID || previous.Intent.Generation != generation {
			return empty, fmt.Errorf("native maintenance request changed")
		}
		return previous.Intent, nil
	}
	if w.book.quarantined || w.lease != nil || w.book.Assignment != nil && w.book.Assignment.State != "stopped" {
		return empty, fmt.Errorf("native maintenance requires an idle slot")
	}
	if w.manager != nil && len(w.manager.GatewayStates()) != 0 {
		return empty, fmt.Errorf("native maintenance slot still owns gateways")
	}
	if w.network != nil {
		w.network.mu.Lock()
		active := w.network.active != nil
		w.network.mu.Unlock()
		if active {
			return empty, fmt.Errorf("native maintenance slot still owns execution resources")
		}
	}
	digest := sha256.Sum256(canonical)
	intent := containerStopIntent{Version: 1, OperationID: operationID, PodUID: w.cfg.PodUID, Slot: w.slotName, GatewayID: fmt.Sprintf("gw-%d-%d", instanceID, generation), InstanceID: instanceID, UserID: userID, Generation: generation, WorkerEpoch: w.workerEpoch, AssignmentDigest: hex.EncodeToString(digest[:]), Kind: "maintenance"}
	previous := *w.book
	w.book.Assignment = &Assignment{PodUID: intent.PodUID, GatewayID: intent.GatewayID, InstanceID: instanceID, UserID: userID, Generation: generation, WorkerEpoch: w.workerEpoch, State: "maintenance"}
	w.book.Digest = intent.AssignmentDigest
	w.book.StopIntent = &intent
	w.book.TerminationProof = nil
	w.book.StopFinalized = false
	w.book.Maintenance = &nativeMaintenanceBook{OperationID: operationID, Operation: canonical, Intent: intent}
	if err = w.book.save(); err != nil {
		*w.book = previous
		return empty, err
	}
	return intent, nil
}
