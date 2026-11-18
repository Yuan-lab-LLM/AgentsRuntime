package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestUpgradeActivationExpectedPodUID(t *testing.T) {
	for _, pod := range []string{"old", "new", ""} {
		t.Run("expected-"+pod, func(t *testing.T) {
			cfg := gateway.Config{RuntimeType: "opencode", WorkspaceRoot: t.TempDir(), PodUID: "new", UpgradeID: "r1", ControlToken: "ctl"}
			manager := gateway.NewGatewayManager(cfg, nil, nil)
			body := `{"rollout_id":"r1"}`
			if pod != "" {
				body = `{"rollout_id":"r1","expected_pod_uid":"` + pod + `"}`
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/openclaw/upgrade/activate", strings.NewReader(body))
			request.Header.Set(ControlTokenHeader, "ctl")
			out := httptest.NewRecorder()
			NewControlHandler(cfg, manager, nil).ServeHTTP(out, request)
			if pod == "old" {
				if out.Code != 409 || !manager.UpgradeStandby() {
					t.Fatalf("stale activation: %d %s", out.Code, out.Body.String())
				}
			} else if out.Code != 200 || manager.UpgradeStandby() {
				t.Fatalf("valid/legacy activation rejected: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
