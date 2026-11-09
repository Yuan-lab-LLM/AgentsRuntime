package hermes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestServeTeamSigningSecretIsStableAndInstanceScoped(t *testing.T) {
	t.Setenv(desktopWebFlag, "true")
	t.Setenv("CLAWMANAGER_HERMES_BACKEND_MODE", "serve")
	req := strictTestRequest(t.TempDir())
	req.Environment["CLAWMANAGER_TEAM_ENABLED"] = "true"
	req.Environment["HERMES_DASHBOARD_BASIC_AUTH_SECRET"] = "short-user-override"
	cfg := gateway.Config{}
	first := NewProfile("hermes").GatewayEnv(nil, cfg, req, req.WorkspacePath, 20000)
	secret := envValue(first, "HERMES_DASHBOARD_BASIC_AUTH_SECRET")
	if len(secret) != 64 || secret != dashboardSigningSecret("managed-auth") {
		t.Fatal("managed signing secret missing or user-controlled")
	}
	if envValue(first, "CLAWMANAGER_TEAM_ENABLED") != "true" || envValue(first, "CLAWMANAGER_HERMES_BACKEND_MODE") != "serve" {
		t.Fatal("serve and Team must coexist")
	}
	req.Generation++
	if envValue(NewProfile("hermes").GatewayEnv(nil, cfg, req, req.WorkspacePath, 20000), "HERMES_DASHBOARD_BASIC_AUTH_SECRET") != secret {
		t.Fatal("generation replacement changed stable instance signing identity")
	}
	req.Environment["CLAWMANAGER_INSTANCE_TOKEN"] = "different-instance"
	if envValue(NewProfile("hermes").GatewayEnv(nil, cfg, req, req.WorkspacePath, 20000), "HERMES_DASHBOARD_BASIC_AUTH_SECRET") == secret {
		t.Fatal("different instances shared signing identity")
	}
}

func TestLegacyHermesContinuousHealthDoesNotRequireLiteWeb(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer s.Close()
	_, raw, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(raw)
	h := newHealthChecker(gateway.Config{GatewayStartupTimeout: time.Second})
	if err := h.CheckHealth(context.Background(), gateway.GatewayStartSpec{Port: port}); err != nil {
		t.Fatal(err)
	}
}
