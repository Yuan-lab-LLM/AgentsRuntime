package agent

import (
	"context"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/internal/control"
	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/shutdownoutbox"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShutdownReplayLostAckAfterGatewayDeletion(t *testing.T) {
	calls := 0
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runtime-agent/shutdown-deliveries" || r.Header.Get("X-ClawManager-Agent-Token") != "pool-token" {
			t.Error("unbound source")
		}
		var batch struct {
			Deliveries []shutdownoutbox.Delivery `json:"deliveries"`
		}
		if json.NewDecoder(r.Body).Decode(&batch) != nil || len(batch.Deliveries) != 1 {
			t.Error("bad batch")
			w.WriteHeader(400)
			return
		}
		calls++
		id := batch.Deliveries[0].ID
		seen[id] = true
		if calls == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": shutdownoutbox.Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{id}}})
	}))
	defer server.Close()
	root := t.TempDir()
	q := shutdownoutbox.New(root, "key", "scope")
	if q.Put(shutdownoutbox.Event{InstanceID: 8, OwnerUserID: 7, Generation: 2, ClaimID: strings.Repeat("a", 64), Outcome: "cleanup_confirmed_release_observed"}) != nil {
		t.Fatal("put")
	}
	a := &Agent{shutdownQueue: q, reporter: control.NewReportClient(gateway.Config{BackendURL: server.URL, ReportToken: "pool-token"})}
	a.reportShutdownDeliveries(context.Background())
	pending, _ := q.Pending()
	if len(pending) != 1 {
		t.Fatal("lost ack deleted data")
	}
	a.shutdownQueue = shutdownoutbox.New(root, "key", "scope")
	a.reportShutdownDeliveries(context.Background())
	pending, err := a.shutdownQueue.Pending()
	if err != nil || len(pending) != 0 || len(seen) != 1 || calls != 2 {
		t.Fatalf("replay failed: %d %d %v", len(pending), calls, err)
	}
}
