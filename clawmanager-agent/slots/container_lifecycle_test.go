package slots

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
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
	force := message
	force.Kind = "force-delete"
	forced, _ := containerTerminationCommitment(force)
	if forced != "C1:753s4hFxuxQB7Dy7eTeFyiPwsJqaEwHmQKibceW93NQ" {
		t.Fatalf("CM/AR force-delete commitment schema drift: %s", forced)
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
	for _, tc := range []struct {
		name     string
		kind     string
		external bool
	}{
		{name: "native stop"},
		{name: "native force delete", kind: "force-delete"},
		{name: "external fence", external: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			intent.Kind = tc.kind
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
			if tc.external {
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
			if tc.kind == "force-delete" {
				stale = proof
				stale.ProofSource = "kubelet-container-fence-v1"
				stale.FenceRequestUID = "request-uid"
				if call(stale) != 409 {
					t.Fatal("external fencing proof was accepted as native force-delete proof")
				}
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
			stale = proof
			stale.ProofSource = "unknown-source"
			data, _ := json.Marshal(stale)
			out := httptest.NewRecorder()
			w.serveContainerRelease(out, httptest.NewRequest(http.MethodPost, "/v1/release-stop", bytes.NewReader(data)))
			if out.Code != 400 || !w.book.quarantined || w.book.StopFinalized {
				t.Fatal("invalid proof source finalized slot reuse")
			}
			data, _ = json.Marshal(proof)
			out = httptest.NewRecorder()
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

		})
	}
}

func forceDeleteWorker(t *testing.T) (*Worker, containerStopIntent) {
	t.Helper()
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", RuntimeType: "opencode", PodUID: "pod", AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir()}
	b, err := openBook(cfg.AgentDataDir)
	if err != nil {
		t.Fatal(err)
	}
	epoch := strings.Repeat("a", 32)
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 1, PodUID: cfg.PodUID}
	if err = b.begin(req, epoch); err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: cfg, book: b, slotName: "slot-0", workerEpoch: epoch, network: &networkOwner{}, manager: gateway.NewGatewayManager(cfg, nil, nil), reset: make(chan struct{})}
	intent := containerStopIntent{Version: 1, OperationID: strings.Repeat("b", 32), PodUID: cfg.PodUID, Slot: w.slotName, GatewayID: "gw-7-1", InstanceID: 7, UserID: 2, Generation: 1, WorkerEpoch: epoch, AssignmentDigest: b.Digest}
	return w, intent
}

func callContainerStop(w *Worker, intent containerStopIntent) int {
	data, _ := json.Marshal(intent)
	out := httptest.NewRecorder()
	w.serveContainerStop(out, httptest.NewRequest(http.MethodDelete, "/v1/gateways/"+intent.GatewayID, bytes.NewReader(data)), intent.GatewayID)
	return out.Code
}

func TestExplicitForceDeleteSupersedesOnlySameCommittedExecution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*containerStopIntent)
		ready    bool
		accepted bool
	}{
		{name: "same execution", accepted: true},
		{name: "different operation", change: func(i *containerStopIntent) { i.OperationID = strings.Repeat("c", 32) }},
		{name: "different epoch", change: func(i *containerStopIntent) { i.WorkerEpoch = strings.Repeat("c", 32) }},
		{name: "different slot", change: func(i *containerStopIntent) { i.Slot = "slot-1" }},
		{name: "different digest", change: func(i *containerStopIntent) { i.AssignmentDigest = strings.Repeat("c", 64) }},
		{name: "graceful force", change: func(i *containerStopIntent) { i.Graceful = true }},
		{name: "implicit escalation", change: func(i *containerStopIntent) { i.Kind = "" }},
		{name: "receipt already committed", ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, old := forceDeleteWorker(t)
			old.Graceful = true
			w.book.StopIntent = &old
			w.resetReady = tc.ready
			next := old
			next.Kind = "force-delete"
			next.Graceful = false
			if tc.change != nil {
				tc.change(&next)
			}
			callContainerStop(w, next)
			expected := old
			if tc.accepted {
				expected = next
			}
			if *w.book.StopIntent != expected {
				t.Fatalf("committed intent=%+v, want %+v", w.book.StopIntent, expected)
			}
			if tc.accepted {
				reopened, err := openBook(w.cfg.AgentDataDir)
				if err != nil || reopened.StopIntent == nil || *reopened.StopIntent != next {
					t.Fatalf("force takeover was not persisted: %+v %v", reopened, err)
				}
				if w.resetReady {
					t.Fatal("force intent became stopped proof without container cleanup")
				}
			}
		})
	}
}

func TestForceDeleteRejectsMaintenanceTakeover(t *testing.T) {
	w, old := forceDeleteWorker(t)
	old.Kind = "maintenance"
	w.book.StopIntent = &old
	next := old
	next.Kind = "force-delete"
	if callContainerStop(w, next) != 409 || *w.book.StopIntent != old {
		t.Fatal("force delete changed native maintenance intent")
	}
}

func TestForceDeleteAcceptsNewAndIdempotentIntentWithoutStopProof(t *testing.T) {
	w, intent := forceDeleteWorker(t)
	intent.Kind = "force-delete"
	for range 2 {
		callContainerStop(w, intent)
		if w.book.StopIntent == nil || *w.book.StopIntent != intent {
			t.Fatal("explicit force intent was rejected")
		}
		if w.book.Assignment.State == "stopped" || w.book.TerminationProof != nil || w.resetReady {
			t.Fatal("force delete bypassed actual termination proof")
		}
	}
}

func TestForceDeleteCancelsInFlightGracefulPreparation(t *testing.T) {
	for _, phase := range []string{"primary preparation", "network cleanup before receipt"} {
		t.Run(phase, func(t *testing.T) {
			w, old := forceDeleteWorker(t)
			cfg := w.cfg
			cfg.Capacity = 1
			cfg.IsolatedLifecycle = true
			cfg.GatewayPortStart = 20000
			cfg.GatewayPortEnd = 20000
			cfg.GatewayPortBlockSize = 1
			cfg.Runtime = generic.NewProfile(cfg.RuntimeType)
			entered := make(chan struct{})
			killed := make(chan struct{})
			canceled := make(chan struct{})
			if phase == "network cleanup before receipt" {
				w.network.active = newTestNetworkSession(context.Background(), func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					close(canceled)
					return nil
				})
			}
			w.manager = gateway.NewGatewayManager(cfg, forceTestStarter{idleTestStarter{start: func() gateway.ManagedProcess {
				return gateway.ManagedProcess{PID: 42, GracefulStop: func(context.Context) error { return errors.New("container termination required") }, Stop: func(context.Context) error { return errors.New("container termination required") }, PrepareReset: func(ctx context.Context, graceful bool) error {
					if !graceful {
						return errors.New("ordinary reset must not force")
					}
					if phase == "network cleanup before receipt" {
						return nil
					}
					close(entered)
					<-ctx.Done()
					close(canceled)
					return nil
				}, PrepareForceReset: func(context.Context) error { close(killed); return nil }}
			}}}, gateway.NewPortAllocator(func(int) bool { return false }))
			req := gateway.CreateGatewayRequest{InstanceID: old.InstanceID, UserID: old.UserID, Generation: old.Generation, PodUID: old.PodUID, AgentType: cfg.RuntimeType, UID: os.Getuid(), GID: os.Getgid(), GatewayPort: 20000, WorkspacePath: filepath.Join(cfg.WorkspaceRoot, cfg.RuntimeType, "user-2", "instance-7")}
			if _, err := w.manager.CreateGateway(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				state, _ := w.manager.GatewayState(old.GatewayID)
				if state.State == "running" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("gateway did not start: %+v", state)
				}
				time.Sleep(time.Millisecond)
			}
			old.Graceful = true
			oldResult := make(chan int, 1)
			go func() { oldResult <- callContainerStop(w, old) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("graceful preparation did not start")
			}
			next := old
			next.Kind = "force-delete"
			next.Graceful = false
			forceResult := make(chan int, 1)
			go func() { forceResult <- callContainerStop(w, next) }()
			select {
			case <-killed:
			case <-time.After(time.Second):
				t.Fatal("force delete waited behind graceful preparation")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("old graceful request was not canceled")
			}
			if code := <-oldResult; code != 409 {
				t.Fatalf("superseded graceful request returned %d", code)
			}
			<-forceResult
			if *w.book.StopIntent != next || w.resetReady || w.book.TerminationProof != nil {
				t.Fatal("old preparation certified or overwrote force-delete intent")
			}

		})
	}
}

type forceTestStarter struct{ idleTestStarter }

func (forceTestStarter) PrepareNativeStorage(context.Context, gateway.CreateGatewayRequest, int) error {
	return nil
}

func TestForceDeletePersistFailureKeepsOldIntentAndPreparation(t *testing.T) {
	w, old := forceDeleteWorker(t)
	old.Graceful = true
	w.book.StopIntent = &old
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.containerPreparation = &containerPreparation{intent: old, cancel: cancel}
	// Persist to a path whose parent is a regular file.
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	w.book.path = filepath.Join(path, "book.json")
	next := old
	next.Kind = "force-delete"
	next.Graceful = false
	if callContainerStop(w, next) != 409 || *w.book.StopIntent != old || ctx.Err() != nil {
		t.Fatal("failed durable CAS changed or canceled the original operation")
	}
}
