package slots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestPoolReportCountsUnavailableSlotUsedAndPreservesHealthyPeer(t *testing.T) {
	heartbeats := make(chan gateway.HeartbeatPayload, 2)
	states := make(chan gateway.GatewayReportPayload, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runtime-agent/register":
			_, _ = w.Write([]byte(`{"pod_id":9}`))
		case "/api/v1/runtime-agent/heartbeat":
			var payload gateway.HeartbeatPayload
			if json.NewDecoder(r.Body).Decode(&payload) == nil {
				select {
				case heartbeats <- payload:
				default:
				}
			}
		case "/api/v1/runtime-agent/gateways/report":
			var payload gateway.GatewayReportPayload
			if json.NewDecoder(r.Body).Decode(&payload) == nil {
				select {
				case states <- payload:
				default:
				}
			}
		}
	}))
	defer backend.Close()
	cfg := gateway.Config{BackendURL: backend.URL, ControlToken: "secret", SandboxProfile: "nfs-rootless-v1", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1, Capacity: 2}
	p, err := NewPool(cfg, 2, func(ctx context.Context, index int, _, path string, _ []byte) (int, []byte, error) {
		if path != "/v1/slot-state" {
			return 503, nil, nil
		}
		if index == 0 {
			<-ctx.Done()
			return 0, nil, ctx.Err()
		}
		return 200, []byte(`{"assignment":{"instance_id":8,"generation":1,"state":"running"},"gateways":[{"gateway_id":"gw-8-1","instance_id":8,"generation":1,"state":"running"}]}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.report(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case heartbeat := <-heartbeats:
		if heartbeat.UsedSlots != 2 || heartbeat.AvailableSlots != 0 {
			t.Fatalf("unavailable reset slot advertised as capacity: %+v", heartbeat)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no bounded heartbeat while a worker was unavailable")
	}
	select {
	case report := <-states:
		if len(report.Gateways) != 1 || report.Gateways[0].GatewayID != "gw-8-1" {
			t.Fatalf("healthy peer disappeared from partial gateway report: %+v", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no partial gateway report while a worker was unavailable")
	}
}
