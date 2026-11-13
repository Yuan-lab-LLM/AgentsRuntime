package slots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestContainerTerminationCommitmentFitsKubeletBudget(t *testing.T) {
	// Shared CM/AR wire vector, independently calculated with Python hashlib.
	message := containerTerminationMessage{containerStopIntent: containerStopIntent{Version: 1, OperationID: strings.Repeat("1", 32), PodUID: "pod-uid", Slot: "slot-0", GatewayID: "gw-7-1", InstanceID: 7, UserID: 2, Generation: 1, WorkerEpoch: strings.Repeat("2", 32), AssignmentDigest: strings.Repeat("d", 64)}, CleanupConfirmed: true}
	commitment, err := containerTerminationCommitment(message)
	if err != nil || len(commitment) != 46 {
		t.Fatalf("termination commitment must fit per-slot kubelet budget: %q %v", commitment, err)
	}
	if commitment != "C1:58uQ7CZv4aGO9zYxjzLA7yAEWSjn406ue0L_0REyGq4" {
		t.Fatalf("CM/AR commitment schema drift: %s", commitment)
	}
	maintenance := message
	maintenance.Kind = "maintenance"
	native, _ := containerTerminationCommitment(maintenance)
	if native != "C1:6_cSrEvIx_mPxa61xMD0eoI9fWWeW7K9Q4ij0hCQX7M" {
		t.Fatalf("CM/AR maintenance commitment schema drift: %s", native)
	}
	for _, mutate := range []func(*containerTerminationMessage){
		func(m *containerTerminationMessage) { m.CleanupConfirmed = false },
		func(m *containerTerminationMessage) { m.WorkerEpoch = strings.Repeat("3", 32) },
		func(m *containerTerminationMessage) { m.OperationID = strings.Repeat("4", 32) },
		func(m *containerTerminationMessage) { m.Generation++ },
		func(m *containerTerminationMessage) { m.AssignmentDigest = strings.Repeat("e", 64) },
		func(m *containerTerminationMessage) { m.Graceful = true },
		func(m *containerTerminationMessage) { m.PodUID = "other-pod" },
		func(m *containerTerminationMessage) { m.Slot = "slot-1" },
		func(m *containerTerminationMessage) { m.InstanceID++ },
		func(m *containerTerminationMessage) { m.UserID++ },
		func(m *containerTerminationMessage) { m.GatewayID = "gw-8-1" },
		func(m *containerTerminationMessage) { m.Version++ },
		func(m *containerTerminationMessage) { m.Kind = "maintenance" },
	} {
		changed := message
		mutate(&changed)
		digest, err := containerTerminationCommitment(changed)
		if err != nil || digest == commitment {
			t.Fatal("termination commitment omitted cleanup or execution identity")
		}
	}
}

func TestContainerRestartRetainsWriterUntilMatchingProofAndLock(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) { testContainerRestartRetainsWriterUntilMatchingProofAndLock(t, external) })
	}
}

func testContainerRestartRetainsWriterUntilMatchingProofAndLock(t *testing.T, external bool) {
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), RuntimeType: "openclaw", PodUID: "pod"}
	b, err := openBook(cfg.AgentDataDir)
	if err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 1, AgentType: cfg.RuntimeType, PodUID: cfg.PodUID}
	oldEpoch := "11111111111111111111111111111111"
	if err = b.begin(req, oldEpoch); err != nil {
		t.Fatal(err)
	}
	intent := containerStopIntent{Version: 1, OperationID: "22222222222222222222222222222222", PodUID: cfg.PodUID, Slot: "slot-0", GatewayID: "gw-7-1", InstanceID: 7, UserID: 2, Generation: 1, WorkerEpoch: oldEpoch, AssignmentDigest: b.Digest}
	b.StopIntent = &intent
	if err = b.save(); err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: cfg, book: b, slotName: "slot-0", workerEpoch: "33333333333333333333333333333333"}
	lockPath, writerPath, err := w.writerPaths(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = claimWriter(writerPath, req, w.slotName); err != nil {
		t.Fatal(err)
	}
	w.book, err = openBook(cfg.AgentDataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !w.book.quarantined || w.book.Assignment.State == "stopped" {
		t.Fatal("restart silently released the old writer")
	}
	proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now()}
	if external {
		proof.ProofSource = "kubelet-container-fence-v1"
		proof.FenceRequestUID = "request-uid"
	}
	call := func(p containerTerminationProof) int {
		data, _ := json.Marshal(p)
		r := httptest.NewRequest(http.MethodPost, "/v1/reconcile-stop", bytes.NewReader(data))
		out := httptest.NewRecorder()
		w.serveContainerReconcile(out, r)
		return out.Code
	}
	stale := proof
	stale.ProofSource = "unknown-source"
	if call(stale) != 409 {
		t.Fatal("unknown fencing source released writer")
	}
	stale = proof
	stale.ProofSource = "kubelet-container-fence-v1"
	stale.FenceRequestUID = ""
	if call(stale) != 409 {
		t.Fatal("missing operator request released writer")
	}
	stale = proof
	stale.OperationID = "44444444444444444444444444444444"
	if call(stale) != 409 {
		t.Fatal("stale stop operation released writer")
	}
	lease, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lockWorkspace(lease); err != nil {
		t.Fatal(err)
	}
	if call(proof) != 409 {
		t.Fatal("reconciliation ignored a live writer lock")
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if call(proof) != 200 {
		t.Fatal("matching terminated-container proof did not reconcile")
	}
	record, err := readWriter(writerPath)
	if err != nil || record.State != "stopped" || w.book.Assignment.State != "stopped" || !w.book.quarantined {
		t.Fatalf("writer reconciliation missing: %+v %v", record, err)
	}
	if call(proof) != 200 {
		t.Fatal("matching proof retry must be idempotent")
	}
	reopened, err := openBook(cfg.AgentDataDir)
	if err != nil || !reopened.quarantined {
		t.Fatal("restart lost pending controller finalization")
	}
	data, _ := json.Marshal(proof)
	out := httptest.NewRecorder()
	w.serveContainerRelease(out, httptest.NewRequest(http.MethodPost, "/v1/release-stop", bytes.NewReader(data)))
	if out.Code != 200 || w.book.quarantined || !w.book.StopFinalized {
		t.Fatal("confirmed controller stop did not finalize slot reuse")
	}
	next := req
	next.InstanceID = 8
	if err = w.book.begin(next, w.workerEpoch); err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	w.serveContainerRelease(out, httptest.NewRequest(http.MethodPost, "/v1/release-stop", bytes.NewReader(data)))
	if out.Code != 200 || w.book.Assignment.InstanceID != 8 || w.book.Assignment.State != "starting" {
		t.Fatal("lost release acknowledgement retry changed new execution")
	}
}
