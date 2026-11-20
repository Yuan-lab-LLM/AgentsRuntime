package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"github.com/iamlovingit/clawmanager-agent/workspacegit"
)

func decodeWorkbench(r *http.Request) (workbench.Request, []byte, error) {
	var req workbench.Request
	if r.Method != "POST" {
		return req, nil, workbench.Invalid()
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, workbench.MaxRequest+1))
	if err != nil || len(raw) > workbench.MaxRequest || json.Unmarshal(raw, &req) != nil || req.InstanceID <= 0 || req.UserID <= 0 || req.Generation <= 0 || req.GatewayID == "" || req.Operation == "" {
		return req, nil, workbench.Invalid()
	}
	return req, raw, nil
}
func workbenchError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	safe := &workbench.Error{Code: "execution_failed", Message: "Business operation failed."}
	var e *workbench.Error
	var git *workspacegit.Error
	switch {
	case errors.Is(err, gateway.ErrStaleGeneration):
		status = 409
		safe = &workbench.Error{Code: "stale_generation", Message: "Instance assignment has changed."}
	case errors.Is(err, gateway.ErrGatewayIdleBusy):
		status = 423
		safe = &workbench.Error{Code: "instance_busy", Message: "Instance is stopping or unavailable."}
	case errors.Is(err, gateway.ErrGatewayStopFailed):
		status = 423
		safe = &workbench.Error{Code: "stop_unconfirmed", Message: "Business process teardown is not confirmed."}
	case errors.Is(err, context.DeadlineExceeded):
		status = 504
		safe = &workbench.Error{Code: "timeout", Message: "Operation timed out."}
	case errors.Is(err, context.Canceled):
		status = 409
		safe = &workbench.Error{Code: "cancelled", Message: "Operation was cancelled."}
	case errors.As(err, &git):
		status = 400
		safe = &workbench.Error{Code: git.Code, Message: git.Message}
	case errors.As(err, &e):
		status = 400
		safe = e
	}
	writeJSON(w, status, map[string]any{"error": safe})
}
func matchesWorkbench(a *Assignment, req workbench.Request) bool {
	return a != nil && a.InstanceID == req.InstanceID && a.UserID == req.UserID && a.Generation == req.Generation && a.GatewayID == req.GatewayID && a.State == "starting" && !a.IdleStop
}

// The worker owns the NFS flock from admission until container teardown. Its
// in-memory claim makes a candidate selectable while an unrelated worker restarts.
func (w *Worker) currentNFSWriter() bool {
	a := w.book.Assignment
	r := w.writerRequest
	return w.cfg.SandboxProfile == "nfs-rootless-v1" && w.lease != nil && a != nil &&
		executionTokenValid(w.workerEpoch) && a.WorkerEpoch == w.workerEpoch &&
		w.cfg.PodUID != "" && a.PodUID == w.cfg.PodUID && r.PodUID == a.PodUID &&
		r.InstanceID == a.InstanceID && r.UserID == a.UserID && r.Generation == a.Generation && r.AgentType == w.cfg.RuntimeType
}

// Recheck the target under its admission lock. A stale local book or replaced
// NFS lock inode cannot authorize a workbench request, even after selection.
func (w *Worker) admitsWorkbench(req workbench.Request) bool {
	if w.book == nil || w.book.quarantined || w.book.StopIntent != nil || w.book.Maintenance != nil ||
		w.book.PendingReset != nil || !matchesWorkbench(w.book.Assignment, req) {
		return false
	}
	if w.cfg.SandboxProfile != "nfs-rootless-v1" {
		return true
	}
	if !w.currentNFSWriter() || w.network.unavailable() {
		return false
	}
	dir := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	base := filepath.Join(dir, fmt.Sprintf("%s-%d-%d", w.cfg.RuntimeType, req.UserID, req.InstanceID))
	if w.lease.Name() != base+".lock" || w.writerPath != base+".json" || trustedDirectory(dir) != nil {
		return false
	}
	info, err := w.lease.Stat()
	if err != nil || sourceProofFileUnchanged(base+".lock", w.lease, info) != nil {
		return false
	}
	file, before, err := openSourceProofFile(w.writerPath, os.O_RDONLY)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	var writer writerRecord
	want := writerRecord{InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, RuntimeType: w.cfg.RuntimeType, PodUID: w.cfg.PodUID, Slot: w.slotName, State: "running"}
	return err == nil && decodeSourceWriterJSON(data, "state", &writer) == nil && writer == want &&
		sourceProofFileUnchanged(w.writerPath, file, before) == nil
}

func (p *Pool) serveWorkbench(w http.ResponseWriter, r *http.Request) {
	req, raw, err := decodeWorkbench(r)
	if err != nil {
		workbenchError(w, err)
		return
	}
	selected, err := p.workbenchSlot(r.Context(), req)
	if err != nil {
		workbenchError(w, err)
		return
	}
	p.forward(w, r, selected, raw)
}
func (w *Worker) serveWorkbench(out http.ResponseWriter, r *http.Request) {
	req, _, err := decodeWorkbench(r)
	if err != nil {
		workbenchError(out, err)
		return
	}
	w.mu.Lock()
	valid := w.admitsWorkbench(req)
	if !valid {
		w.mu.Unlock()
		workbenchError(out, gateway.ErrStaleGeneration)
		return
	}
	result, err := w.manager.WorkbenchWithAdmission(r.Context(), req, w.mu.Unlock)
	if err != nil {
		workbenchError(out, err)
		return
	}
	writeJSON(out, 200, result)
}

func (p *Pool) workbenchSlot(ctx context.Context, req workbench.Request) (int, error) {
	p.mu.RLock()
	draining := p.draining
	p.mu.RUnlock()
	if draining {
		return -1, gateway.ErrGatewayIdleBusy
	}
	snapshotCtx := ctx
	if p.cfg.SandboxProfile == "nfs-rootless-v1" {
		var cancel context.CancelFunc
		snapshotCtx, cancel = context.WithTimeout(ctx, time.Second)
		defer cancel()
	}
	snapshots, healthy := p.snapshots(snapshotCtx)
	selected := -1
	matches := 0
	for i, snapshot := range snapshots {
		if a := snapshot.Assignment; a != nil && a.InstanceID == req.InstanceID && a.State != "stopped" {
			matches++
			// A known conflicting claim still counts. When another slot cannot
			// answer, only an exclusive current NFS writer may serve this scope.
			proven := healthy == p.count || p.cfg.SandboxProfile == "nfs-rootless-v1" && snapshot.NFSWriterOwned &&
				snapshot.PodUID == p.cfg.PodUID && a.PodUID == snapshot.PodUID &&
				executionTokenValid(snapshot.WorkerEpoch) && a.WorkerEpoch == snapshot.WorkerEpoch
			if proven && !snapshot.Quarantined && matchesWorkbench(a, req) {
				for _, g := range snapshot.Gateways {
					if g.GatewayID == req.GatewayID && g.InstanceID == req.InstanceID && g.UserID == req.UserID && g.Generation == req.Generation && g.State == "running" {
						selected = i
					}
				}
			}
		}
	}
	if selected < 0 || matches != 1 {
		return -1, gateway.ErrStaleGeneration
	}
	return selected, nil
}
