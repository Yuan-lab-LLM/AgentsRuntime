package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenClawNativeReadyStillRequiresCapabilityReport(t *testing.T) {
	ready := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready {
			w.Write([]byte(healthyCapabilityJSON))
		} else {
			w.Write([]byte(`{"protocol":1,"generation":7,"enrolled":false,"enrollment_generation":null,"registration_is_sticky":true,"current_report":null}`))
		}
	}))
	defer upstream.Close()
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true,"ready":true}`)) }))
	defer native.Close()
	for _, kind := range []string{"openclaw", "openclaw-shell"} {
		h := NewHTTPGatewayHealthChecker(Config{RuntimeType: kind, OpenClawVersion: "2026.10.2"})
		spec := GatewayStartSpec{Port: native.Listener.Addr().(*net.TCPAddr).Port, Env: []string{taskProtocolFlag + "=1", renewalFlag + "=1", "CLAWMANAGER_LLM_BASE_URL=" + upstream.URL, "CLAWMANAGER_LLM_API_KEY=test-key", "CLAWMANAGER_GATEWAY_GENERATION=7"}}
		ready = false
		if h.CheckHealth(context.Background(), spec) == nil {
			t.Fatal("native ready bypassed missing enrollment")
		}
		ready = true
		if err := h.CheckHealth(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		spec.Env = nil
		ready = false
		if err := h.CheckHealth(context.Background(), spec); err != nil {
			t.Fatal("legacy mode changed", err)
		}
	}
}
