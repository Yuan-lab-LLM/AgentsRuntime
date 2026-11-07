package slots

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestWorkerIdleProofRejectsUnfinishedExecution(t *testing.T) {
	cfg := gateway.Config{PodUID: "current-pod", ControlToken: "control", WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1, Capacity: 1}
	w, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	read := func() bool {
		r := httptest.NewRequest("GET", "/v1/slot-state", nil)
		r.Header.Set(gateway.ControlTokenHeader, "control")
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		var s struct {
			Idle  bool   `json:"idle_confirmed"`
			Pod   string `json:"pod_uid"`
			Epoch string `json:"worker_epoch"`
		}
		if json.Unmarshal(out.Body.Bytes(), &s) != nil || s.Pod != "current-pod" || s.Epoch == "" {
			t.Fatalf("missing scoped idle proof: %s", out.Body.String())
		}
		return s.Idle
	}
	if !read() {
		t.Fatal("fresh empty worker should prove idle")
	}
	w.book.Assignment = &Assignment{InstanceID: 2, GatewayID: "gw-2-1", State: "starting"}
	if read() {
		t.Fatal("active owner advertised idle")
	}
	w.book.Assignment.State = "stopped"
	if !read() {
		t.Fatal("confirmed legacy stopped owner should be idle")
	}
	w.book.StopIntent = &containerStopIntent{}
	if read() {
		t.Fatal("unfinished container stop advertised idle")
	}
	w.book.StopFinalized = true
	if !read() {
		t.Fatal("finalized empty worker should be idle")
	}
	w.book.quarantined = true
	if read() {
		t.Fatal("quarantined worker advertised idle")
	}
	w.book.quarantined = false
	lease, err := os.CreateTemp(t.TempDir(), "writer-")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	w.lease = lease
	if read() {
		t.Fatal("held writer lease advertised idle")
	}
	w.lease = nil
	w.network.active = &networkSession{ctx: context.Background()}
	if read() {
		t.Fatal("live networking advertised idle")
	}
	w.network.active = nil
}
