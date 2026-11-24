package slots

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestCancelUnstartedNativeReservation(t *testing.T) {
	if os.Geteuid() != 10001 {
		t.Skip("requires reserved management identity")
	}
	cfg := gateway.Config{WorkspaceRoot: t.TempDir(), RuntimeType: "openclaw", PodUID: "old-pod", ControlToken: "control", SandboxProfile: "nfs-rootless-v1", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	id := nativeReservationIdentity{OperationID: strings.Repeat("a", 32), PodUID: cfg.PodUID, Action: "compatibility", InstanceID: 2, UserID: 1, Generation: 3}
	for _, tc := range []struct {
		name   string
		owner  string
		status int
	}{
		{name: "unreachable slot", status: http.StatusServiceUnavailable},
		{name: "launched slot", owner: id.OperationID, status: http.StatusOK},
		{name: "never started", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.WorkspaceRoot = t.TempDir()
			call := func(_ context.Context, _ int, _, path string, _ []byte) (int, []byte, error) {
				if path != "/v1/slot-state" {
					t.Fatalf("cancellation dispatched %s", path)
				}
				raw, _ := json.Marshal(Snapshot{MaintenanceOperationID: tc.owner})
				return tc.status, raw, nil
			}
			p, err := NewPool(cfg, 1, call)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := p.cancelUnstartedNativeReservation(t.Context(), id)
			if tc.owner != "" || tc.status != http.StatusOK {
				if err == nil {
					t.Fatal("ambiguous or running operation was cancelled")
				}
				return
			}
			if err != nil || receipt.nativeReservationIdentity != id {
				t.Fatalf("expected exact receipt, got %+v %v", receipt, err)
			}
			restarted, err := NewPool(cfg, 1, call)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := restarted.cancelUnstartedNativeReservation(t.Context(), id)
			if err != nil || replay != receipt {
				t.Fatalf("durable replay changed: %+v %v", replay, err)
			}
			op := gateway.NativeUpgradeOperation{OperationID: id.OperationID, Action: id.Action, Request: gateway.WorkspaceUpgradeRequest{RolloutID: "r1", InstanceID: id.InstanceID, UserID: id.UserID, Generation: id.Generation, UID: 200002, GID: 200002}}
			if _, err := restarted.startNativeMaintenance(t.Context(), op); err == nil || !strings.Contains(err.Error(), "reservation is cancelled") {
				t.Fatalf("late start was not rejected: %v", err)
			}
		})
	}
}
