package opencode

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestV2HealthProbeRequiresAuthenticatedServerInfo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"ready", 200, `{"version":"2.0.27","pid":42}`, false},
		{"wrong endpoint", 404, `not found`, true},
		{"unauthorized", 401, `{}`, true},
		{"login redirect", 302, ``, true},
		{"html fallback", 200, `<html></html>`, true},
		{"not ready", 200, `{"version":"2.0.27","pid":0}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/info" {
					t.Errorf("path = %s", r.URL.Path)
				}
				user, password, ok := r.BasicAuth()
				if !ok || user != "opencode" || password != "secret" {
					t.Error("missing managed authentication")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(u.Port())
			checker := newHealthChecker(gateway.Config{}).(*healthChecker)
			err := checker.probeOnce(t.Context(), gateway.GatewayStartSpec{Port: port, Env: []string{"OPENCODE_RUNTIME_MAJOR=2", "OPENCODE_SERVER_PASSWORD=secret"}})
			if (err != nil) != tc.wantError {
				t.Fatalf("probe error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}
