package process

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenClaw102ProAdmissionMatchesLiteBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, version, startup, ready string
		want                          bool
	}{
		{"ready", "2026.10.2", `{"ok":true}`, `{"ready":true}`, true},
		{"inspection pending", "2026.10.2", `{"ok":false}`, `{"ready":true}`, false},
		{"database failure", "2026.10.2", `{"ok":true}`, `{"ready":false}`, false},
		{"UI is not an admission response", "2026.10.2", `<html>UI</html>`, `{"ready":true}`, false},
		{"old behavior unchanged", "2026.8.1", `invalid`, `legacy 200 body`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", tc.version)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/startupz" {
					_, _ = w.Write([]byte(tc.startup))
				} else {
					_, _ = w.Write([]byte(tc.ready))
				}
			}))
			defer server.Close()
			if got := gatewayAdmissionReady(context.Background(), server.Client(), server.URL+"/readyz"); got != tc.want {
				t.Fatalf("ready=%v want=%v", got, tc.want)
			}
		})
	}
}
