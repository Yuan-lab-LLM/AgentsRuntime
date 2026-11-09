package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-openclaw-image/lite"
)

type capabilityProfile struct {
	gateway.RuntimeProfile
	value *gateway.HealthCapabilities
}

func (p capabilityProfile) HealthCapabilities() *gateway.HealthCapabilities { return p.value }

func TestControlHealthOptionalCapabilitySnapshot(t *testing.T) {
	cfg := testConfig(t)
	capability := &gateway.HermesDesktopWebCapability{ContractVersion: 2, Enabled: true, HermesRef: "v2026.8.31", HermesCommit: "29112bef099274229cadff79cdff7bf7b99c4b77", RPCProtocol: "hermes-jsonrpc-v1", BackendMode: "dashboard", AuthMode: "password-cookie", ArtifactsVerified: true, ReleaseAccepted: false, PayloadSHA256: strings.Repeat("a", 64)}
	cfg.Runtime = capabilityProfile{RuntimeProfile: openclaw.NewProfile("openclaw"), value: &gateway.HealthCapabilities{HermesDesktopWeb: capability}}
	starter := &fakeStarter{nextPID: 4242}
	mgr := NewGatewayManager(cfg, starter, NewPortAllocator(func(int) bool { return false }))
	capability.Enabled = false // Provider-owned memory cannot mutate the manager snapshot.
	copy := mgr.HealthCapabilities()
	copy.HermesDesktopWeb.Enabled = false
	copy.HermesDesktopWeb.ReleaseAccepted = true
	copy.HermesDesktopWeb.ArtifactsVerified = false
	copy.HermesDesktopWeb.PayloadSHA256 = "changed"
	handler := NewControlHandler(cfg, mgr, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
			r.Header.Set(ControlTokenHeader, cfg.ControlToken)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var result gateway.HealthResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Capabilities == nil || !result.Capabilities.HermesDesktopWeb.Enabled {
				t.Error("capability startup snapshot changed")
			} else if c := result.Capabilities.HermesDesktopWeb; c.ContractVersion != 2 || !c.ArtifactsVerified || c.ReleaseAccepted || c.PayloadSHA256 != strings.Repeat("a", 64) {
				t.Error("capability integrity or acceptance snapshot changed")
			}
			var legacy struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(w.Body.Bytes(), &legacy) != nil || legacy.Status != "ready" {
				t.Error("old client cannot read health")
			}
		}()
	}
	wg.Wait()
	if starter.startCount() != 0 || len(mgr.GatewayStates()) != 0 {
		t.Fatal("health created user gateways")
	}
	mgr.SetDraining(true)
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	r.Header.Set(ControlTokenHeader, cfg.ControlToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var result gateway.HealthResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Status != "draining" || result.Capabilities == nil {
		t.Fatal("capability overrode drain")
	}
}

func TestControlHealthCapabilityKeepsLegacyErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method, token string
		broken              bool
		want                int
	}{
		{"missing token", http.MethodGet, "", false, 401}, {"wrong token", http.MethodGet, "wrong", false, 401},
		{"wrong method", http.MethodPost, "valid", false, 405}, {"base unavailable", http.MethodGet, "valid", true, 503},
		{"old profile", http.MethodGet, "valid", false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			if tc.broken {
				cfg.WorkspaceRoot = ""
			}
			mgr := NewGatewayManager(cfg, &fakeStarter{}, NewPortAllocator(func(int) bool { return false }))
			r := httptest.NewRequest(tc.method, "/v1/health", nil)
			token := tc.token
			if token == "valid" {
				token = cfg.ControlToken
			}
			r.Header.Set(ControlTokenHeader, token)
			w := httptest.NewRecorder()
			NewControlHandler(cfg, mgr, nil).ServeHTTP(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "capabilities") || strings.Contains(w.Body.String(), cfg.ControlToken) {
				t.Fatalf("health changed legacy semantics: status=%d", w.Code)
			}
		})
	}
}
