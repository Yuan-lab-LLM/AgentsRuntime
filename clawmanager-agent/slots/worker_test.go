package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/http/httptest"
	"testing"
)

func TestWorkerStateRequiresTokenAndUnknownStopIsNotConfirmed(t *testing.T) {
	cfg := gateway.Config{ControlToken: "control", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3, Capacity: 1}
	worker, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, token string
		want                int
	}{{"GET", "/v1/slot-state", "", 401}, {"GET", "/v1/slot-state", "control", 200}, {"DELETE", "/v1/gateways/gw-1-1", "control", 409}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set(gateway.ControlTokenHeader, test.token)
		w := httptest.NewRecorder()
		worker.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("%s: status=%d body=%s", test.path, w.Code, w.Body.String())
		}
	}
}

func TestWorkerHealthAllowsCompleteMultiportSlot(t *testing.T) {
	cfg := gateway.Config{ControlToken: "control", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3, Capacity: 1}
	worker, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.manager.Health(); err != nil {
		t.Fatalf("valid multiport slot unhealthy: %v", err)
	}
}
