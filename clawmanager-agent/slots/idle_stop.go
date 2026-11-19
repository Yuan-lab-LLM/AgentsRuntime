package slots

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func idleOperation(path string) (id, operation string) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/gateways/"), "/")
	if !strings.HasPrefix(path, "/v1/gateways/") || len(parts) != 2 || (parts[1] != "idle-stop" && parts[1] != "idle-preflight") {
		return "", ""
	}
	return parts[0], parts[1]
}

func decodeIdleRequest(body []byte, id, podUID string) (gateway.IdleStopRequest, error) {
	var req gateway.IdleStopRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 4096 || decoder.Decode(&req) != nil || !req.Valid(id, podUID) {
		return req, gateway.ErrGatewayStopConflict
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return req, gateway.ErrGatewayStopConflict
	}
	return req, nil
}

func (w *Worker) serveIdle(out http.ResponseWriter, r *http.Request, id, operation string) {
	if w.cfg.SandboxProfile == "nfs-rootless-v1" && operation == "idle-stop" {
		http.Error(out, "graceful idle stop requires the container intent protocol", 409)
		return
	}
	if r.Method != http.MethodPost {
		out.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	req, decodeErr := decodeIdleRequest(body, id, w.cfg.PodUID)
	if err != nil || decodeErr != nil {
		http.Error(out, "invalid idle-stop identity", 400)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.book.Assignment
	if w.book.quarantined || a == nil || a.GatewayID != id || a.InstanceID != req.InstanceID || a.Generation != req.Generation || a.PodUID != req.PodUID {
		writeJSON(out, 409, map[string]any{"error": "idle-stop source identity is unknown", "execution_state": "unknown", "execution_proof_supported": false})
		return
	}
	if operation == "idle-preflight" {
		proof, err := w.manager.IdlePreflight(r.Context(), id, req)
		if err != nil {
			writeJSON(out, 409, map[string]any{"error": "idle preflight unavailable", "execution_state": "unknown", "execution_proof_supported": false})
			return
		}
		writeJSON(out, 200, proof)
		return
	}
	if a.State == "stopped" {
		if !a.IdleStop {
			http.Error(out, "previous stop did not establish graceful idle proof", 409)
			return
		}
		writeIdleStopped(out, id, req)
		return
	}
	err = w.manager.IdleStopGateway(r.Context(), id, req, func() error {
		a.IdleStop = true
		if err := w.book.save(); err != nil {
			a.IdleStop = false
			return err
		}
		return nil
	})
	if err != nil {
		state := "unknown"
		if errors.Is(err, gateway.ErrGatewayIdleUnsupported) {
			state = "unsupported"
		} else if errors.Is(err, gateway.ErrGatewayIdleBusy) {
			state = "busy"
		}
		writeJSON(out, 409, map[string]any{"error": "graceful idle stop is not confirmed", "execution_state": state, "execution_proof_supported": state != "unsupported"})
		return
	}
	if err = stopWriter(w.writerPath, w.writerRequest, w.slotName); err != nil {
		http.Error(out, "failed to persist writer stop", 409)
		return
	}
	if err = w.book.stopped(id); err != nil {
		http.Error(out, "failed to persist idle stop confirmation", 409)
		return
	}
	if w.lease != nil {
		if err = w.lease.Close(); err != nil {
			w.book.quarantined = true
			http.Error(out, "failed to release writer lease", 409)
			return
		}
		w.lease = nil
	}
	writeIdleStopped(out, id, req)
}

func writeIdleStopped(out http.ResponseWriter, id string, req gateway.IdleStopRequest) {
	writeJSON(out, 200, map[string]any{"gateway_id": id, "instance_id": req.InstanceID, "generation": req.Generation, "pod_uid": req.PodUID, "stopped": true, "confirmed": true, "unmounted": true, "graceful": true})
}

func (w *Worker) idleStopFenced() bool {
	return w.book.Assignment != nil && w.book.Assignment.IdleStop && w.book.Assignment.State != "stopped"
}
