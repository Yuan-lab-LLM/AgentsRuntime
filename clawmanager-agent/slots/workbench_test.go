package slots

import (
	"context"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPoolWorkbenchRequiresUniqueActiveAssignment(t *testing.T) {
	for _, mode := range []string{"valid", "stale", "duplicate", "quarantined", "stopping", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}
			p, _ := NewPool(cfg, 2, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
				if path == "/v1/slot-state" {
					snapshot := Snapshot{}
					if index == 1 || mode == "duplicate" {
						snapshot.Assignment = &Assignment{InstanceID: 1, UserID: 2, Generation: 3, GatewayID: "gw-1-3", State: "starting"}
						snapshot.Gateways = []gateway.GatewayState{{InstanceID: 1, UserID: 2, Generation: 3, GatewayID: "gw-1-3", State: "running"}}
						snapshot.Quarantined = mode == "quarantined"
						if mode == "stopping" {
							snapshot.Assignment.State = "stopping"
						}
						if mode == "unknown" {
							snapshot.Assignment = nil
						}
						if mode == "stale" {
							snapshot.Assignment.Generation = 4
						}
					}
					b, _ := json.Marshal(snapshot)
					return 200, b, nil
				}
				calls++
				if index != 1 {
					t.Error("wrong slot")
				}
				return 200, []byte(`{"git":true}`), nil
			})
			r := httptest.NewRequest("POST", "/v1/workbench", strings.NewReader(`{"instance_id":1,"user_id":2,"generation":3,"gateway_id":"gw-1-3","operation":"capabilities","data":{}}`))
			r.Header.Set(gateway.ControlTokenHeader, "secret")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if mode == "valid" {
				if w.Code != 200 || calls != 1 {
					t.Fatalf("%d %d %s", w.Code, calls, w.Body.String())
				}
			} else if w.Code != 409 || calls != 0 {
				t.Fatalf("unsafe routing: %d %d %s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestWorkbenchLargeJSONOutputUsesTrustedLimit(t *testing.T) {
	var capture boundedCommandOutput
	capture.stdout.limit = 2 << 20
	payload := strings.Repeat("a", 100<<10)
	capture.Stdout().Write([]byte(payload))
	got, err := capture.Result(nil)
	if err != nil || string(got) != payload {
		t.Fatalf("workbench JSON truncated: %d %v", len(got), err)
	}
	var old boundedCommandOutput
	old.Stdout().Write([]byte(payload))
	if _, err = old.Result(nil); err == nil {
		t.Fatal("old callers lost bounded default")
	}
}
