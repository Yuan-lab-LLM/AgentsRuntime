package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestWorkerProfileMismatchBeforeWriterAcquisition(t *testing.T) {
	for _, profiles := range [][2]string{{"isolated-network-v1", ""}, {"", "isolated-network-v1"}, {"unknown", "unknown"}} {
		cfg := gateway.Config{RuntimeType: "hermes", ControlToken: "control", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), Capacity: 1, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, NetworkSecurityProfile: profiles[0]}
		w, err := NewWorker(cfg)
		if err != nil {
			continue
		}
		req := gateway.CreateGatewayRequest{InstanceID: 1, UserID: 1, Generation: 1, UID: 200001, GID: 200001, AgentType: "hermes", GatewayPort: 20000, WorkspacePath: filepath.Join(cfg.WorkspaceRoot, "hermes", "user-1", "instance-1"), SecurityProfile: profiles[1]}
		b, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/v1/gateways", bytes.NewReader(b))
		r.Header.Set(gateway.ControlTokenHeader, "control")
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		if out.Code != 400 || w.lease != nil || w.book.Assignment != nil {
			t.Fatalf("profile %q/%q acquired writer: status=%d lease=%v assignment=%v", profiles[0], profiles[1], out.Code, w.lease, w.book.Assignment)
		}
	}
}

func TestNetworkFailureAdvertisesQuarantine(t *testing.T) {
	cfg := gateway.Config{ControlToken: "control", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), Capacity: 1, GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	w, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.network.active = newTestNetworkSession(ctx, nil)
	cancel()
	r := httptest.NewRequest("GET", "/v1/slot-state", nil)
	r.Header.Set(gateway.ControlTokenHeader, "control")
	out := httptest.NewRecorder()
	w.ServeHTTP(out, r)
	var snapshot Snapshot
	if err = json.Unmarshal(out.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Quarantined {
		t.Fatal("lost session advertised reusable slot")
	}
}
