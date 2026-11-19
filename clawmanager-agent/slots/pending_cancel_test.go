package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func pendingWorker(t *testing.T) *Worker {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWorker(gateway.Config{ControlToken: "control", RuntimeType: "openclaw", PodUID: "pod-1", WorkspaceRoot: root, AgentDataDir: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	return w
}
func pendingScope() map[string]any {
	return map[string]any{"instance_id": 7, "user_id": 2, "generation": 3, "pod_uid": "pod-1", "agent_type": "openclaw", "gateway_port": 20000}
}
func pendingCall(handler http.Handler, scope map[string]any, token string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(scope)
	r := httptest.NewRequest("POST", "/v1/gateways/cancel-pending", bytes.NewReader(body))
	r.Header.Set(gateway.ControlTokenHeader, token)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	return out
}
func pendingCreate(w *Worker) gateway.CreateGatewayRequest {
	return gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 3, PodUID: "pod-1", AgentType: "openclaw", GatewayPort: 20000, UID: 200001, GID: 200001, WorkspacePath: filepath.Join(w.cfg.WorkspaceRoot, "openclaw", "user-2", "instance-7")}
}
func TestPendingCancellationPersistsFenceWithoutAssignment(t *testing.T) {
	w := pendingWorker(t)
	out := pendingCall(w, pendingScope(), "control")
	if out.Code != 200 {
		t.Fatalf("cancel status=%d body=%s", out.Code, out.Body)
	}
	var receipt struct {
		GatewayID                     string `json:"gateway_id"`
		Stopped, Confirmed, Unmounted bool
	}
	if json.Unmarshal(out.Body.Bytes(), &receipt) != nil || receipt.GatewayID != "gw-7-3" || !receipt.Stopped || !receipt.Confirmed || !receipt.Unmounted {
		t.Fatalf("receipt %s", out.Body)
	}
	if w.book.Assignment != nil || w.lease != nil {
		t.Fatal("pending cancellation invented live assignment")
	}
	req := pendingCreate(w)
	if lease, err := w.acquire(req); err == nil {
		lease.Close()
		t.Fatal("late create bypassed canceled generation")
	}
	restored, err := NewWorker(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := restored.acquire(req); err == nil {
		lease.Close()
		t.Fatal("restart lost cancellation")
	}
	otherCfg := w.cfg
	otherCfg.AgentDataDir = t.TempDir()
	otherCfg.GatewayPortStart = 20003
	otherCfg.GatewayPortEnd = 20005
	other, err := NewWorker(otherCfg)
	if err != nil {
		t.Fatal(err)
	}
	req.GatewayPort = 20003
	if lease, err := other.acquire(req); err == nil {
		lease.Close()
		t.Fatal("another slot bypassed cancellation")
	}
}
func TestPendingCancellationScopeAndActiveWriter(t *testing.T) {
	for _, mutation := range []map[string]any{{"pod_uid": "wrong"}, {"agent_type": "hermes"}, {"gateway_port": 20001}, {"user_id": 0}, {"instance_id": 0}, {"generation": 0}, {"extra": true}} {
		w := pendingWorker(t)
		scope := pendingScope()
		for k, v := range mutation {
			scope[k] = v
		}
		if got := pendingCall(w, scope, "control").Code; got != 400 {
			t.Fatalf("scope %v status %d", mutation, got)
		}
	}
	w := pendingWorker(t)
	if got := pendingCall(w, pendingScope(), "").Code; got != 401 {
		t.Fatalf("unauth status %d", got)
	}
	req := pendingCreate(w)
	lease, err := w.acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	w.lease = lease
	if err = w.book.begin(req); err != nil {
		t.Fatal(err)
	}
	if got := pendingCall(w, pendingScope(), "control").Code; got != 409 {
		t.Fatalf("active cancellation status %d", got)
	}
	record, err := readWriter(w.writerPath)
	if err != nil || record.State != "running" {
		t.Fatal("active writer changed")
	}
}
func TestPoolPendingCancellationRoutesOnlyExactOriginalSlot(t *testing.T) {
	calls := 0
	cfg := gateway.Config{ControlToken: "control", RuntimeType: "openclaw", PodUID: "pod-1", GatewayPortStart: 20000, GatewayPortEnd: 20005, GatewayPortBlockSize: 3}
	p, err := NewPool(cfg, 2, func(_ context.Context, slot int, method, path string, body []byte) (int, []byte, error) {
		calls++
		if slot != 1 || method != "POST" || path != "/v1/gateways/cancel-pending" {
			t.Errorf("wrong routing %d %s %s", slot, method, path)
		}
		return 200, []byte(`{"gateway_id":"gw-7-3","stopped":true,"confirmed":true,"unmounted":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := pendingScope()
	scope["gateway_port"] = 20003
	if got := pendingCall(p, scope, "control").Code; got != 200 || calls != 1 {
		t.Fatalf("routing status=%d calls=%d", got, calls)
	}
	for _, port := range []int{19999, 20001, 20006} {
		scope["gateway_port"] = port
		if got := pendingCall(p, scope, "control").Code; got != 400 {
			t.Fatalf("bad port status %d", got)
		}
	}
	for _, mutation := range []map[string]any{{"pod_uid": "wrong"}, {"agent_type": "hermes"}, {"generation": 0}, {"user_id": 0}, {"instance_id": 0}, {"unknown": true}} {
		invalid := pendingScope()
		invalid["gateway_port"] = 20003
		for key, value := range mutation {
			invalid[key] = value
		}
		if got := pendingCall(p, invalid, "control").Code; got != 400 {
			t.Fatalf("pool scope %v status %d", mutation, got)
		}
	}
	if got := pendingCall(p, pendingScope(), "").Code; got != 401 {
		t.Fatalf("pool unauth status %d", got)
	}
	if calls != 1 {
		t.Fatal("invalid scope reached workers")
	}
}

func TestPendingCancellationPreservesOtherLiveWriterAndSlotReuse(t *testing.T) {
	w := pendingWorker(t)
	other := pendingCreate(w)
	other.InstanceID = 8
	other.WorkspacePath = filepath.Join(w.cfg.WorkspaceRoot, "openclaw", "user-2", "instance-8")
	lease, err := w.acquire(other)
	if err != nil {
		t.Fatal(err)
	}
	w.lease = lease
	if err = w.book.begin(other); err != nil {
		t.Fatal(err)
	}
	beforePath, beforeRequest, beforeAssignment := w.writerPath, w.writerRequest, w.book.Assignment
	if out := pendingCall(w, pendingScope(), "control"); out.Code != 200 {
		t.Fatalf("cancel unrelated pending: %d", out.Code)
	}
	if w.lease != lease || w.writerPath != beforePath || w.writerRequest.InstanceID != beforeRequest.InstanceID || w.book.Assignment != beforeAssignment {
		t.Fatal("other live writer bookkeeping replaced")
	}
	record, err := readWriter(beforePath)
	if err != nil || record.State != "running" {
		t.Fatal("other live writer stopped")
	}
	if err = w.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := pendingCreate(w)
	req.Generation++
	newer, err := w.acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	w.lease = newer
	if err = w.book.begin(req); err != nil {
		t.Fatal(err)
	}
	if out := pendingCall(w, pendingScope(), "control"); out.Code != 409 {
		t.Fatal("cancellation disturbed newer active writer")
	}
}

func TestPendingCancellationRefusesUncertainOwnership(t *testing.T) {
	for _, state := range []string{"quarantine", "network", "running", "upgrading", "locked"} {
		t.Run(state, func(t *testing.T) {
			w := pendingWorker(t)
			req := pendingCreate(w)
			if state == "quarantine" {
				w.book.quarantined = true
			}
			if state == "network" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				w.network.active = &networkSession{ctx: ctx}
			}
			if state == "running" || state == "upgrading" || state == "locked" {
				lockPath, recordPath, err := w.writerPaths(req)
				if err != nil {
					t.Fatal(err)
				}
				record := writerRecord{InstanceID: 7, UserID: 2, Generation: 2, RuntimeType: "openclaw", PodUID: "previous-pod", Slot: "slot-1", State: state}
				if state == "locked" {
					record.State = "stopped"
					f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					if err = lockWorkspace(f); err != nil {
						t.Fatal(err)
					}
				}
				if err = atomicJSON(recordPath, record); err != nil {
					t.Fatal(err)
				}
			}
			if out := pendingCall(w, pendingScope(), "control"); out.Code != 409 {
				t.Fatalf("uncertain state %s accepted: %d", state, out.Code)
			}
		})
	}
}

func TestPendingCancellationSaveFailureAndHighwater(t *testing.T) {
	w := pendingWorker(t)
	req := pendingCreate(w)
	path := filepath.Join(t.TempDir(), "missing", "writer.json")
	if err := cancelPendingWriter(path, req, "slot-0"); err == nil {
		t.Fatal("failed durable save acknowledged")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed save left record")
	}
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := claimWriter(path, req, "slot-0"); err != nil {
		t.Fatalf("failed cancellation invented highwater: %v", err)
	}
	if err := cancelPendingWriter(path, req, "slot-0"); err == nil {
		t.Fatal("active writer overwritten")
	}
	if err := stopWriter(path, req, "slot-0"); err != nil {
		t.Fatal(err)
	}
	req.Generation = 5
	if err := cancelPendingWriter(path, req, "slot-0"); err != nil {
		t.Fatal(err)
	}
	req.Generation = 4
	if err := cancelPendingWriter(path, req, "slot-0"); err != nil {
		t.Fatal(err)
	}
	record, err := readWriter(path)
	if err != nil || record.Generation != 5 || record.State != "stopped" {
		t.Fatal("cancellation lowered fence")
	}
	for _, generation := range []int{3, 4, 5} {
		req.Generation = generation
		if err := claimWriter(path, req, "slot-1"); err == nil {
			t.Fatal("canceled generation admitted")
		}
	}
	req.Generation = 6
	if err := claimWriter(path, req, "slot-1"); err != nil {
		t.Fatal(err)
	}
}

func TestPendingCancellationSerializesWithCreateAdmission(t *testing.T) {
	w := pendingWorker(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	w.handler = http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { close(entered); <-release; out.WriteHeader(200) })
	body, _ := json.Marshal(pendingCreate(w))
	request := httptest.NewRequest("POST", "/v1/gateways", bytes.NewReader(body))
	request.Header.Set(gateway.ControlTokenHeader, "control")
	createDone := make(chan int, 1)
	go func() { out := httptest.NewRecorder(); w.ServeHTTP(out, request); createDone <- out.Code }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("create did not enter")
	}
	cancelDone := make(chan int, 1)
	go func() { cancelDone <- pendingCall(w, pendingScope(), "control").Code }()
	select {
	case code := <-cancelDone:
		t.Fatalf("cancellation bypassed admission lock: %d", code)
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if code := <-createDone; code != 200 {
		t.Fatalf("create status %d", code)
	}
	if code := <-cancelDone; code != 409 {
		t.Fatalf("active create cancellation %d", code)
	}
	// Only the existing stop path confirms cleanup, then pending cancel is safe.
	stopped := httptest.NewRecorder()
	stop := httptest.NewRequest("DELETE", "/v1/gateways/gw-7-3", nil)
	stop.Header.Set(gateway.ControlTokenHeader, "control")
	w.ServeHTTP(stopped, stop)
	if stopped.Code != 200 {
		t.Fatalf("confirmed stop failed %s", stopped.Body)
	}
	if got := pendingCall(w, pendingScope(), "control").Code; got != 200 {
		t.Fatalf("post-stop cancellation %d", got)
	}
	late := httptest.NewRequest("POST", "/v1/gateways", bytes.NewReader(body))
	late.Header.Set(gateway.ControlTokenHeader, "control")
	out := httptest.NewRecorder()
	w.ServeHTTP(out, late)
	if out.Code != 409 {
		t.Fatalf("late duplicate create %d", out.Code)
	}
}
