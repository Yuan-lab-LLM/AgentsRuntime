package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
)

type forceHTTPStarter struct{}

func (forceHTTPStarter) StartGateway(context.Context, gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	return gateway.ManagedProcess{PID: 42, Stop: func(context.Context) error { return nil }}, nil
}

func TestForceDeleteHTTPFailClosedWithoutDedicatedProof(t *testing.T) {
	cfg := gateway.Config{RuntimeType: "opencode", Runtime: generic.NewProfile("opencode"), WorkspaceRoot: t.TempDir(), ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000}
	m := gateway.NewGatewayManager(cfg, forceHTTPStarter{}, gateway.NewPortAllocator(func(int) bool { return false }))
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 1, Generation: 1, AgentType: "opencode", WorkspacePath: filepath.Join(cfg.WorkspaceRoot, "opencode", "user-1", "instance-7")}
	response, err := m.CreateGateway(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, _ := m.GatewayState(response.GatewayID)
		if state.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not running: %+v", state)
		}
		time.Sleep(time.Millisecond)
	}
	h := NewControlHandler(cfg, m, nil)
	for _, tc := range []struct {
		query, token string
		status       int
	}{
		{"?force_delete=true", "", 401},
		{"?force_delete=true", "secret", 501},
		{"", "secret", 200},
	} {
		r := httptest.NewRequest(http.MethodDelete, "/v1/gateways/"+response.GatewayID+tc.query, nil)
		r.Header.Set(ControlTokenHeader, tc.token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		if out.Code != tc.status {
			t.Fatalf("query=%s token=%t got=%d want=%d body=%s", tc.query, tc.token != "", out.Code, tc.status, out.Body.String())
		}
	}
}
