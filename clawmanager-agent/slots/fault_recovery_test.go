package slots

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func faultRecoveryFixture(t *testing.T) (*Worker, map[string]any, string) {
	t.Helper()
	w, intent := forceDeleteWorker(t)
	w.book.StopIntent = &intent
	w.cfg.ControlToken = "control"
	w.handler = http.NotFoundHandler()
	w.workerEpoch = strings.Repeat("b", 32)
	w.book.quarantined = true
	scope := map[string]any{"version": 1, "operation_id": "runtime_mgmt_fault-test", "namespace": "cm", "pod_name": "runtime-pod", "pod_uid": w.cfg.PodUID, "node_name": "node", "node_uid": "node-uid", "node_boot_id": "boot", "container_name": w.slotName, "container_id": "containerd://old", "runtime_type": w.cfg.RuntimeType, "gateway_id": "gw-7-1", "workspace_root": w.cfg.WorkspaceRoot, "instance_id": 7, "user_id": 2, "generation": 1}
	raw, _ := json.Marshal(scope)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	receipt := map[string]any{"scope_digest": digest, "operation_id": scope["operation_id"], "pod_uid": w.cfg.PodUID, "container_name": w.slotName, "node_uid": "node-uid", "node_boot_id": "boot", "stopped_container_ids": []string{"containerd://old"}, "ledger_digest": strings.Repeat("d", 64), "finished_at": time.Now().UTC().Truncate(time.Second), "confirmed": true}
	dir := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".recovery-"+digest+".json")
	raw, _ = json.Marshal(receipt)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	proof := map[string]any{"scope": scope, "receipt": receipt, "worker_epoch": w.book.Assignment.WorkerEpoch, "assignment_digest": w.book.Digest, "current_worker_epoch": w.workerEpoch}
	if err := w.book.save(); err != nil {
		t.Fatal(err)
	}
	return w, proof, path
}
func callFaultRecovery(w *Worker, action string, proof any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(proof)
	r := httptest.NewRequest(http.MethodPost, "/v1/"+action, bytes.NewReader(raw))
	r.Header.Set("X-ClawManager-Control-Token", w.cfg.ControlToken)
	// Use the same control header as every existing worker endpoint.
	out := httptest.NewRecorder()
	w.ServeHTTP(out, r)
	return out
}
func TestFaultRecoveryReleaseSurvivesRestartAndProtectsNewOwner(t *testing.T) {
	w, proof, _ := faultRecoveryFixture(t)
	// A current writer elsewhere must not be stopped or rewritten by this release.
	marker := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks", w.cfg.RuntimeType+"-2-7.json")
	newest := []byte(`{"instance_id":7,"user_id":2,"generation":3,"runtime_type":"opencode","pod_uid":"new-pod","slot":"slot-1","state":"running"}`)
	if err := os.WriteFile(marker, newest, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(strings.TrimSuffix(marker, ".json")+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = lockWorkspace(lock); err != nil {
		t.Fatal(err)
	}
	call := func(action string) {
		t.Helper()
		out := callFaultRecovery(w, action, proof)
		if out.Code != 200 {
			t.Fatalf("%s: %d %s", action, out.Code, out.Body.String())
		}
	}
	call("reconcile-fault-stop")
	if w.book.Assignment.State != "stopped" || !w.book.quarantined || w.idleConfirmed() {
		t.Fatal("reconcile released before controller finalization")
	}
	b, err := openBook(w.cfg.AgentDataDir)
	if err != nil || !b.quarantined {
		t.Fatal("restart lost pending fault finalization")
	}
	w.book = b
	call("reconcile-fault-stop")
	call("release-fault-stop")
	if w.book.StopFinalized || w.book.TerminationProof != nil || w.book.StopIntent == nil {
		t.Fatal("fault authority replaced native termination evidence")
	}
	if !w.idleConfirmed() {
		t.Fatal("released fault slot is not reusable")
	}
	b, err = openBook(w.cfg.AgentDataDir)
	if err != nil || b.quarantined {
		t.Fatal("release was not durable")
	}
	w.book = b
	call("release-fault-stop")
	after, _ := os.ReadFile(marker)
	if !bytes.Equal(after, newest) {
		t.Fatal("release touched newer writer")
	}
	next := *w.book.Assignment
	next.InstanceID = 8
	next.GatewayID = "gw-8-1"
	next.State = "running"
	next.WorkerEpoch = w.workerEpoch
	w.book.Assignment = &next
	w.book.Digest = strings.Repeat("e", 64)
	if err = w.book.save(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"reconcile-fault-stop"} {
		out := callFaultRecovery(w, action, proof)
		if out.Code != 409 {
			t.Fatalf("old %s accepted new owner: %d", action, out.Code)
		}
	}
	// Another recovery overwrites the book's last marker, but cannot discard
	// the immutable release receipt for the earlier operation.
	w.book.LastFaultRelease = nil
	call("release-fault-stop")
	if w.book.Assignment.InstanceID != 8 || w.book.Assignment.State != "running" {
		t.Fatal("old receipt changed new owner")
	}
}
func TestFaultRecoveryRejectsUnprovenOrLiveExecution(t *testing.T) {
	for _, kind := range []string{"missing-receipt", "changed-receipt", "symlink-receipt", "wrong-generation", "wrong-digest", "same-worker", "release-before-reconcile", "persist-failure"} {
		t.Run(kind, func(t *testing.T) {
			w, proof, path := faultRecoveryFixture(t)
			action := "reconcile-fault-stop"
			switch kind {
			case "missing-receipt":
				os.Remove(path)
			case "changed-receipt":
				os.WriteFile(path, []byte(`{}`), 0600)
			case "symlink-receipt":
				raw, _ := os.ReadFile(path)
				os.Remove(path)
				target := filepath.Join(t.TempDir(), "receipt")
				os.WriteFile(target, raw, 0600)
				os.Symlink(target, path)
			case "wrong-generation":
				proof["scope"].(map[string]any)["generation"] = 2
			case "wrong-digest":
				proof["assignment_digest"] = strings.Repeat("e", 64)
			case "same-worker":
				w.workerEpoch = w.book.Assignment.WorkerEpoch
			case "release-before-reconcile":
				action = "release-fault-stop"
			case "persist-failure":
				w.book.path = filepath.Join(t.TempDir(), "missing", "book.json")
			}
			out := callFaultRecovery(w, action, proof)
			if out.Code != 409 && out.Code != 400 {
				t.Fatalf("unsafe %s accepted: %d %s", kind, out.Code, out.Body.String())
			}
			if w.book.Assignment.State == "stopped" || !w.book.quarantined {
				t.Fatal("failed proof changed old execution")
			}
		})
	}
}
