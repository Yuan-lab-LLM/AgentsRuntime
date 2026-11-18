package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func slotResetFixture(t *testing.T) (*Worker, map[string]any) {
	t.Helper()
	w, intent := forceDeleteWorker(t)
	w.cfg.ControlToken, w.cfg.Namespace, w.cfg.PodName = "control", "runtime", "pool"
	w.handler = http.NotFoundHandler()
	w.workerEpoch = strings.Repeat("c", 32)
	w.book.StopIntent = &intent
	w.book.quarantined = true
	if err := w.book.save(); err != nil {
		t.Fatal(err)
	}
	id := "containerd://" + strings.Repeat("1", 64)
	scope := json.RawMessage(fmt.Sprintf(`{"version":1,"operation_id":"reset-123","namespace":"runtime","pod_name":"pool","pod_uid":"pod","node_name":"node","node_uid":"uid","node_boot_id":"boot","container_name":"slot-0","container_id":"%s"}`, id))
	receipt := map[string]any{"scope_digest": recoveryHash(scope), "operation_id": "reset-123", "pod_uid": "pod", "node_uid": "uid", "node_boot_id": "boot", "container_name": "slot-0", "container_id": id, "finished_at": time.Now().UTC(), "confirmed": true}
	return w, map[string]any{"scope": scope, "receipt": receipt, "worker_epoch": intent.WorkerEpoch, "assignment_digest": w.book.Digest, "current_worker_epoch": w.workerEpoch}
}

func resetCall(t *testing.T, w *Worker, proof any, action string, want int) {
	t.Helper()
	cfg := w.cfg
	cfg.GatewayPortStart, cfg.GatewayPortEnd, cfg.GatewayPortBlockSize = 20000, 20000, 1
	p, err := NewPool(cfg, 1, func(ctx context.Context, slot int, method, path string, data []byte) (int, []byte, error) {
		if slot != 0 {
			t.Fatalf("forwarded to slot %d", slot)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		return out.Code, out.Body.Bytes(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/slots/slot-0/"+action, bytes.NewReader(data))
	r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
	out := httptest.NewRecorder()
	p.ServeHTTP(out, r)
	if out.Code != want {
		t.Fatalf("%s: got %d, expected %d: %s", action, out.Code, want, out.Body)
	}
	if want != 200 {
		return
	}
	var ack struct {
		OperationID string `json:"operation_id"`
		ScopeDigest string `json:"scope_digest"`
		ContainerID string `json:"container_id"`
		WorkerEpoch string `json:"worker_epoch"`
		Stopped     bool   `json:"stopped"`
		Confirmed   bool   `json:"confirmed"`
		Released    bool   `json:"released"`
	}
	payload := proof.(map[string]any)
	receipt := payload["receipt"].(map[string]any)
	if json.Unmarshal(out.Body.Bytes(), &ack) != nil || ack.OperationID != receipt["operation_id"] || ack.ScopeDigest != receipt["scope_digest"] || ack.ContainerID != receipt["container_id"] || ack.WorkerEpoch != w.workerEpoch || !ack.Stopped || !ack.Confirmed || ack.Released != (action == "release-reset") {
		t.Fatalf("bad acknowledgement: %s", out.Body)
	}
}

func TestPhysicalSlotResetLeavesSameGenerationMigratedWriterAndReplaysAfterRestart(t *testing.T) {
	w, proof := slotResetFixture(t)
	dir := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writer := filepath.Join(dir, "opencode-2-7.json")
	record := []byte(`{"instance_id":7,"user_id":2,"generation":1,"runtime_type":"opencode","pod_uid":"pod","slot":"slot-1","state":"running"}`)
	if err := os.WriteFile(writer, record, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "opencode-2-7.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = lockWorkspace(lock); err != nil {
		t.Fatal(err)
	}
	resetCall(t, w, proof, "release-reset", 409)
	resetCall(t, w, proof, "reconcile-reset", 200)
	w.book, err = openBook(w.cfg.AgentDataDir)
	if err != nil || !w.book.quarantined || w.idleConfirmed() {
		t.Fatal("lost pending quarantine", err)
	}
	w.workerEpoch = strings.Repeat("d", 32)
	proof["current_worker_epoch"] = w.workerEpoch
	resetCall(t, w, proof, "reconcile-reset", 200)
	resetCall(t, w, proof, "release-reset", 200)
	w.book, err = openBook(w.cfg.AgentDataDir)
	if err != nil || !w.idleConfirmed() {
		t.Fatal("lost durable physical release", err)
	}
	if w.book.StopFinalized || w.book.TerminationProof != nil || w.book.Assignment.IdleStop {
		t.Fatal("physical stop produced graceful or native completion")
	}
	resetCall(t, w, proof, "release-reset", 200)
	next := gateway.CreateGatewayRequest{InstanceID: 8, UserID: 2, Generation: 1, PodUID: w.cfg.PodUID}
	if err = w.book.begin(next, w.workerEpoch); err != nil {
		t.Fatal(err)
	}
	resetCall(t, w, proof, "reconcile-reset", 409)
	resetCall(t, w, proof, "release-reset", 200)
	if w.book.Assignment.InstanceID != 8 || w.book.Assignment.State != "starting" {
		t.Fatal("replay changed successor")
	}
	changed, err := os.ReadFile(writer)
	if err != nil || !bytes.Equal(changed, record) {
		t.Fatal("changed migrated writer", err)
	}
}

func TestPhysicalSlotResetEmptyBookSurvivesRestart(t *testing.T) {
	w, p := slotResetFixture(t)
	w.book = &book{path: w.book.path}
	if err := w.book.save(); err != nil {
		t.Fatal(err)
	}
	p["worker_epoch"], p["assignment_digest"] = "", ""
	resetCall(t, w, p, "reconcile-reset", 200)
	var err error
	w.book, err = openBook(w.cfg.AgentDataDir)
	if err != nil || !w.book.quarantined || w.idleConfirmed() {
		t.Fatal("empty book was released early", err)
	}
	resetCall(t, w, p, "release-reset", 200)
	w.book, err = openBook(w.cfg.AgentDataDir)
	if err != nil || !w.idleConfirmed() {
		t.Fatal("empty book release lost", err)
	}
}

func TestPhysicalSlotResetRejectsUnknownOrActiveOwnerAndChangedProof(t *testing.T) {
	for _, kind := range []string{"unknown-owner", "current-worker", "wrong-digest", "wrong-new-worker", "namespace", "container", "receipt-digest", "unconfirmed", "maintenance", "lease", "malformed-book", "reconcile-save", "release-save"} {
		t.Run(kind, func(t *testing.T) {
			w, p := slotResetFixture(t)
			r := p["receipt"].(map[string]any)
			action := "reconcile-reset"
			switch kind {
			case "unknown-owner":
				p["worker_epoch"], p["assignment_digest"] = "", ""
			case "current-worker":
				w.book.Assignment.WorkerEpoch = w.workerEpoch
				p["worker_epoch"] = w.workerEpoch
			case "wrong-digest":
				p["assignment_digest"] = strings.Repeat("f", 64)
			case "wrong-new-worker":
				p["current_worker_epoch"] = strings.Repeat("f", 32)
			case "namespace":
				w.cfg.Namespace = "different"
			case "container":
				r["container_id"] = "containerd://" + strings.Repeat("2", 64)
			case "receipt-digest":
				r["scope_digest"] = strings.Repeat("2", 64)
			case "unconfirmed":
				r["confirmed"] = false
			case "maintenance":
				w.book.Maintenance = &nativeMaintenanceBook{Uncertain: true}
			case "lease":
				w.lease = os.Stdin
			case "malformed-book":
				w.book.Assignment.InstanceID = 0
			case "release-save":
				resetCall(t, w, p, "reconcile-reset", 200)
				action = "release-reset"
				fallthrough
			case "reconcile-save":
				w.book.path = filepath.Join(t.TempDir(), "unwritable")
				if err := os.Mkdir(w.book.path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := json.Marshal(w.book)
			if err != nil {
				t.Fatal(err)
			}
			resetCall(t, w, p, action, 409)
			after, _ := json.Marshal(w.book)
			if !w.book.quarantined || !bytes.Equal(before, after) {
				t.Fatal("failed operation changed local book")
			}
			if kind == "release-save" {
				if err = os.Remove(w.book.path); err != nil {
					t.Fatal(err)
				}
				resetCall(t, w, p, "release-reset", 200)
				if !w.idleConfirmed() {
					t.Fatal("could not resume after immutable receipt was saved first")
				}
			}
		})
	}
}

func TestPhysicalSlotResetQuarantinesUnreadableOwnerlessBook(t *testing.T) {
	for _, corrupt := range []string{`{"assignment_v2":{"instance_id":7}}`, `null`, `{"digest":"orphan"}`} {
		w, p := slotResetFixture(t)
		if err := os.WriteFile(w.book.path, []byte(corrupt), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := openBook(w.cfg.AgentDataDir)
		if err != nil {
			continue
		} // An unreadable book may also prevent worker startup.
		w.book = b
		if !b.quarantined || w.idleConfirmed() {
			t.Fatalf("unrecognized book admitted as free: %s", corrupt)
		}
		p["worker_epoch"], p["assignment_digest"] = "", ""
		resetCall(t, w, p, "reconcile-reset", 409)
	}
}
