package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestConfirmedStopHTTPBoundaries(t *testing.T) {
	cfg := gateway.Config{RuntimeType: "opencode", WorkspaceRoot: t.TempDir(), ControlToken: "test-control-token"}
	m := gateway.NewGatewayManager(cfg, nil, gateway.NewPortAllocator(func(int) bool { return false }))
	h := NewControlHandler(cfg, m, nil)
	for _, test := range []struct {
		path, method, token, body string
		status                    int
	}{
		{"/v1/stop-capabilities", "GET", "", "", 401},
		{"/v1/gateway-stop", "POST", "", "{}", 401},
		{"/v1/gateway-stop", "DELETE", cfg.ControlToken, "", 405},
		{"/v1/stop-capabilities", "POST", cfg.ControlToken, "", 405},
		{"/v1/stop-capabilities", "GET", cfg.ControlToken, "", 200},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, "{}", 400},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, "null", 400},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, strings.Repeat("x", 4097), 400},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, `{"instance_id":12,"user_id":4,"generation":0,"gateway_id":"gw-12-0","operation_id":"0123456789abcdef0123456789abcdef","pod_uid":"p","agent_epoch":"e","timeout_seconds":1}`, 501},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, `{"instance_id":12,"user_id":4,"gateway_id":"gw-12-0","operation_id":"0123456789abcdef0123456789abcdef","pod_uid":"p","agent_epoch":"e","timeout_seconds":1}`, 400},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, `{"instance_id":12,"user_id":4,"generation":0,"gateway_id":"gw-12-0","operation_id":"0123456789abcdef0123456789abcdef","pod_uid":"p","agent_epoch":"e","timeout_seconds":1} {}`, 400},
		{"/v1/gateway-stop", "POST", cfg.ControlToken, `{"instance_id":12,"user_id":4,"generation":0,"gateway_id":"gw-12-0","operation_id":"0123456789abcdef0123456789abcdef","pod_uid":"p","agent_epoch":"e","timeout_seconds":1,"force":true}`, 400},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set(ControlTokenHeader, test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Errorf("%s %s got %d: %s", test.method, test.path, w.Code, w.Body.String())
		}
	}
	cfg.ControlToken = ""
	h = NewControlHandler(cfg, m, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/stop-capabilities", nil))
	if w.Code != 503 {
		t.Fatalf("empty token accepted: %d", w.Code)
	}
}

func TestStopConflictHTTPMapping(t *testing.T) {
	w := httptest.NewRecorder()
	writeCreateGatewayError(w, gateway.ErrGatewayStopConflict)
	if w.Code != http.StatusConflict {
		t.Fatal(w.Code)
	}
}
