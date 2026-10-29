package slots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestPoolPreservesFractionalCPUAllocation(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", RuntimeType: "openclaw", PodUID: "pod-1", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	forwarded := false
	p, err := NewPool(cfg, 1, func(_ context.Context, _ int, _, _ string, body []byte) (int, []byte, error) {
		var req gateway.CreateGatewayRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if float64(req.CPUCores) != 0.5 {
			t.Fatalf("fractional CPU lost: %v", req.CPUCores)
		}
		forwarded = true
		return 200, []byte(`{"gateway_id":"gw-1-1"}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/gateways", strings.NewReader(`{"instance_id":1,"user_id":1,"generation":1,"agent_type":"openclaw","pod_uid":"pod-1","gateway_port":20000,"cpu_cores":0.5}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || !forwarded {
		t.Fatalf("status=%d forwarded=%v body=%s", w.Code, forwarded, w.Body.String())
	}
}

func TestPoolRoutesExactReservedPortAndRejectsPartialBlock(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", RuntimeType: "openclaw", GatewayPortStart: 20000, GatewayPortEnd: 20005, GatewayPortBlockSize: 3, Capacity: 2}
	selected := -1
	call := func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		selected = index
		var req gateway.CreateGatewayRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.PortRange.Start != 20003 || req.PortRange.End != 20005 {
			t.Fatalf("slot range: %+v", req.PortRange)
		}
		return 200, []byte(`{"gateway_id":"gw-7-1","state":"starting","gateway_port":20003}`), nil
	}
	p, err := NewPool(cfg, 2, call)
	if err != nil {
		t.Fatal(err)
	}
	request := func(port int) *httptest.ResponseRecorder {
		body, _ := json.Marshal(gateway.CreateGatewayRequest{AgentType: "openclaw", InstanceID: 7, UserID: 2, Generation: 1, GatewayPort: port})
		r := httptest.NewRequest(http.MethodPost, "/v1/gateways", strings.NewReader(string(body)))
		r.Header.Set(gateway.ControlTokenHeader, "secret")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	if w := request(20003); w.Code != 200 || selected != 1 {
		t.Fatalf("status=%d slot=%d body=%s", w.Code, selected, w.Body.String())
	}
	selected = -1
	if w := request(20004); w.Code != 400 || selected != -1 {
		t.Fatalf("unaligned block accepted: status=%d slot=%d", w.Code, selected)
	}
}

func TestPoolAuthenticatesAndDoesNotConfirmUnknownGatewayStop(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	calls := 0
	p, err := NewPool(cfg, 1, func(_ context.Context, _ int, _ string, _ string, _ []byte) (int, []byte, error) {
		calls++
		return 200, []byte(`{"gateways":[]}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodDelete, "/v1/gateways/gw-7-1", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 401 || calls != 0 {
		t.Fatalf("unauthenticated status=%d calls=%d", w.Code, calls)
	}
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("unknown writer stop must fail closed: %d %s", w.Code, w.Body.String())
	}
}

func TestPoolDrainBlocksCreateBeforeForwarding(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	p, _ := NewPool(cfg, 1, func(_ context.Context, _ int, _ string, _ string, _ []byte) (int, []byte, error) {
		return 200, []byte(`{}`), nil
	})
	for _, v := range []struct {
		method, path, body string
		status             int
	}{{"POST", "/v1/drain", `{"draining":true}`, 200}, {"POST", "/v1/gateways", `{"instance_id":1,"generation":1,"gateway_port":20000}`, 409}} {
		r := httptest.NewRequest(v.method, v.path, strings.NewReader(v.body))
		r.Header.Set(gateway.ControlTokenHeader, "secret")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != v.status {
			t.Fatalf("%s status=%d %s", v.path, w.Code, w.Body.String())
		}
	}
}

func TestUpgradePoolDoesNotBecomeReadyBeforeActivation(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, UpgradeID: "rollout-7", AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir()}
	p, err := NewPool(cfg, 1, func(context.Context, int, string, string, []byte) (int, []byte, error) {
		return 200, []byte(`{"gateways":[]}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatalf("standby pool accepted traffic: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/openclaw/upgrade/activate", strings.NewReader(`{"rollout_id":"rollout-7"}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("activation failed: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatalf("activated pool not ready: %d", w.Code)
	}
}

func TestPoolRoutesInstanceControlToLiveWriterBeforeOldReceipt(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1, Capacity: 2}
	selected := -1
	p, _ := NewPool(cfg, 2, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if path == "/v1/slot-state" {
			if index == 0 {
				return 200, []byte(`{"assignment":{"instance_id":7,"generation":1,"state":"stopped"}}`), nil
			}
			return 200, []byte(`{"assignment":{"instance_id":7,"generation":2,"state":"starting"},"gateways":[{"instance_id":7,"generation":2,"gateway_id":"gw-7-2","state":"running"}]}`), nil
		}
		selected = index
		return 200, []byte(`{}`), nil
	})
	r := httptest.NewRequest("POST", "/v1/openclaw/preflight", strings.NewReader(`{"instance_id":7}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || selected != 1 {
		t.Fatalf("routed to stopped predecessor: status=%d slot=%d", w.Code, selected)
	}
}
