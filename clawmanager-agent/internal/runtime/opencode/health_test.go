package opencode

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

func TestHealthCheckerReadyWithBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			http.NotFound(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	checker := newHealthChecker(gateway.Config{GatewayStartupTimeout: 3 * time.Second})
	err = checker.WaitReady(t.Context(), gateway.GatewayStartSpec{
		Port: port,
		Env: []string{
			"OPENCODE_SERVER_USERNAME=opencode",
			"OPENCODE_SERVER_PASSWORD=secret",
		},
	})
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:secret"))
	_ = wantAuth
}
