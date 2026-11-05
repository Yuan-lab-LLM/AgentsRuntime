package slots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestPoolForwardsIdleProtocolWithoutFallback(t *testing.T) {
	for _, suffix := range []string{"idle-stop", "idle-preflight"} {
		t.Run(suffix, func(t *testing.T) {
			forwarded := 0
			p, _ := NewPool(gateway.Config{ControlToken: "secret", PodUID: "pod", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}, 2, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
				if path == "/v1/slot-state" {
					if index == 1 {
						return 200, []byte(`{"assignment":{"gateway_id":"gw-7-2","instance_id":7,"generation":2,"state":"starting"}}`), nil
					}
					return 200, []byte(`{}`), nil
				}
				forwarded++
				if index != 1 || method != "POST" || path != "/v1/gateways/gw-7-2/"+suffix || string(body) != `{"instance_id":7,"generation":2,"pod_uid":"pod"}` {
					t.Fatalf("wrong forwarding index=%d method=%s path=%s body=%s", index, method, path, body)
				}
				return 404, []byte(`{"error":"old worker"}`), nil
			})
			r := httptest.NewRequest("POST", "/v1/gateways/gw-7-2/"+suffix, strings.NewReader(`{"instance_id":7,"generation":2,"pod_uid":"pod"}`))
			r.Header.Set(gateway.ControlTokenHeader, "secret")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != 404 || forwarded != 1 {
				t.Fatalf("status=%d forwarded=%d body=%s", w.Code, forwarded, w.Body.String())
			}
		})
	}
}

func TestWorkerIdleProtocolStrictIdentityAndUnsupported(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", PodUID: "pod", RuntimeType: "openclaw", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), Capacity: 1, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	w, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.book.Assignment = &Assignment{GatewayID: "gw-7-2", InstanceID: 7, Generation: 2, PodUID: "pod", State: "starting"}
	for _, test := range []struct {
		body string
		want int
	}{
		{`{"instance_id":7,"generation":2,"pod_uid":"pod","force":true}`, 400},
		{`{"instance_id":7,"generation":2,"pod_uid":"pod"} {}`, 400},
		{`{"instance_id":7,"generation":2,"pod_uid":"other"}`, 400},
		{`{"instance_id":7,"generation":3,"pod_uid":"pod"}`, 400},
		{`{"instance_id":7,"pod_uid":"pod"}`, 400},
		{`{"instance_id":7,"generation":2,"pod_uid":"pod"}`, 409},
	} {
		r := httptest.NewRequest("POST", "/v1/gateways/gw-7-2/idle-stop", strings.NewReader(test.body))
		r.Header.Set(gateway.ControlTokenHeader, "secret")
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		if out.Code != test.want {
			t.Fatalf("body=%s status=%d response=%s", test.body, out.Code, out.Body.String())
		}
		if w.book.Assignment.State == "stopped" || w.book.Assignment.IdleStop {
			t.Fatal("unknown proof mutated writer state")
		}
	}
}

func TestWorkerIdleStopRetryChecksIdentityAndPolicy(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", PodUID: "pod", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), Capacity: 1, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	w, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.book.Assignment = &Assignment{GatewayID: "gw-7-2", InstanceID: 7, Generation: 2, PodUID: "pod", State: "stopped", IdleStop: true}
	r := httptest.NewRequest("POST", "/v1/gateways/gw-7-2/idle-stop", strings.NewReader(`{"instance_id":7,"generation":2,"pod_uid":"pod"}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	out := httptest.NewRecorder()
	w.ServeHTTP(out, r)
	var result map[string]any
	if json.Unmarshal(out.Body.Bytes(), &result) != nil || out.Code != 200 || result["graceful"] != true || result["confirmed"] != true || result["unmounted"] != true || result["pod_uid"] != "pod" || result["instance_id"] != float64(7) {
		t.Fatalf("status=%d response=%s", out.Code, out.Body.String())
	}
	w.book.Assignment.State = "starting"
	r = httptest.NewRequest(http.MethodDelete, "/v1/gateways/gw-7-2", nil)
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	out = httptest.NewRecorder()
	w.ServeHTTP(out, r)
	if out.Code != 409 {
		t.Fatalf("idle stop fence allowed manual fallback: %d", out.Code)
	}
	if err = w.Shutdown(context.Background()); err == nil {
		t.Fatal("shutdown discarded pending idle proof")
	}
}

type idleTestStarter struct{ start func() gateway.ManagedProcess }

func (s idleTestStarter) StartGateway(context.Context, gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	return s.start(), nil
}

func TestWorkerIdleStopPersistsProofBeforeReleasingWriter(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", PodUID: "pod", RuntimeType: "openclaw", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), Capacity: 1, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	cfg.WorkspaceRoot, _ = filepath.EvalSymlinks(cfg.WorkspaceRoot)
	worker, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 1, Generation: 2, PodUID: "pod", AgentType: "openclaw", UID: os.Getuid(), GID: os.Getgid(), GatewayPort: 20000, WorkspacePath: filepath.Join(cfg.WorkspaceRoot, "openclaw", "user-1", "instance-7")}
	worker.lease, err = worker.acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if worker.lease != nil {
			worker.lease.Close()
		}
	}()
	if err = worker.book.begin(req); err != nil {
		t.Fatal(err)
	}
	graceful := false
	cfg.IsolatedLifecycle = true
	// The Worker always enables isolated lifecycle when constructing its manager.
	worker.manager = gateway.NewGatewayManager(cfg, idleTestStarter{start: func() gateway.ManagedProcess {
		return gateway.ManagedProcess{PID: 1, Stop: func(context.Context) error { t.Error("forceful stop"); return nil }, GracefulStop: func(context.Context) error {
			persisted, e := openBook(cfg.AgentDataDir)
			if e != nil || persisted.Assignment == nil || !persisted.Assignment.IdleStop || worker.lease == nil {
				t.Error("signal before durable fence and writer lease")
			}
			graceful = true
			return nil
		}}
	}}, gateway.NewPortAllocator(func(int) bool { return false }))
	if _, err = worker.manager.CreateGateway(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, _ := worker.manager.GatewayState("gw-7-2")
		if state.State == "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r := httptest.NewRequest("POST", "/v1/gateways/gw-7-2/idle-stop", strings.NewReader(`{"instance_id":7,"generation":2,"pod_uid":"pod"}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	out := httptest.NewRecorder()
	worker.ServeHTTP(out, r)
	if out.Code != 200 || !graceful || worker.lease != nil {
		t.Fatalf("status=%d graceful=%v lease=%v body=%s", out.Code, graceful, worker.lease, out.Body.String())
	}
	persisted, err := openBook(cfg.AgentDataDir)
	if err != nil || persisted.Assignment.State != "stopped" || persisted.quarantined {
		t.Fatalf("assignment=%+v err=%v", persisted, err)
	}
	if err = worker.book.begin(req); err == nil {
		t.Fatal("stopped idle generation was readmitted")
	}
	older := req
	older.Generation--
	if err = worker.book.begin(older); err == nil {
		t.Fatal("older idle generation was readmitted")
	}
	newer := req
	newer.Generation++
	if err = worker.book.begin(newer); err != nil {
		t.Fatalf("newer idle generation blocked by worker: %v", err)
	}
}
