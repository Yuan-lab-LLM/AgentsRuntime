package control

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyPolicyControlRequiresAuthenticationAndRejectsMalformedUpdates(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProxyPolicyFile = filepath.Join(t.TempDir(), "policy.json")
	mgr := NewGatewayManager(cfg, &fakeStarter{}, NewPortAllocator(func(int) bool { return false }))
	h := NewControlHandler(cfg, mgr, &fakeReporter{})
	for _, token := range []string{"", "wrong"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/proxy-policy", nil)
		r.Header.Set(ControlTokenHeader, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized policy read returned %d", w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPut, "/v1/proxy-policy", strings.NewReader(`{"namespace":"other","proxies":["0.0.0.0/0"]}`))
	r.Header.Set(ControlTokenHeader, cfg.ControlToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe update returned %d", w.Code)
	}
}
