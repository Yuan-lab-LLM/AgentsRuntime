package openclaw

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net"
	"net/http"
	"testing"
)

func TestOpenClaw102HealthRequiresNativeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, version, runtimeType, startup, readiness string
		wantErr                                        bool
	}{
		{"ready", "2026.10.2", "openclaw", `{"ok":true}`, `{"ready":true}`, false},
		{"database pending despite UI", "2026.10.2", "openclaw", `{"ok":false,"pendingReason":"agent-database-inspection"}`, `{"ready":true}`, true},
		{"database failed", "2026.10.2", "openclaw", `{"ok":true}`, `{"ready":false,"failing":["agent-database:main"]}`, true},
		{"wrong response", "2026.10.2", "openclaw", `{"ok":true}`, `<html>UI</html>`, true},
		{"missing ready flag", "2026.10.2", "openclaw", `{"ok":true}`, `{}`, true},
		{"old core unchanged", "2026.8.1", "openclaw", `invalid`, `invalid`, false},
		{"other runtime unchanged", "2026.10.2", "hermes", `invalid`, `invalid`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/startupz":
					_, _ = w.Write([]byte(tc.startup))
				case "/readyz":
					_, _ = w.Write([]byte(tc.readiness))
				default:
					_, _ = w.Write([]byte("UI available"))
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Shutdown(context.Background())
			checker := newHealthChecker(gateway.Config{RuntimeType: tc.runtimeType, OpenClawVersion: tc.version})
			err = checker.CheckHealth(context.Background(), gateway.GatewayStartSpec{Port: listener.Addr().(*net.TCPAddr).Port})
			if (err != nil) != tc.wantErr {
				t.Fatalf("health error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
