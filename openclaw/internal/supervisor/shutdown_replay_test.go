package supervisor

import (
	"context"
	"encoding/json"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/control"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/shutdownoutbox"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShutdownReplayRetainsLostAckAndRecoversAfterRecreation(t *testing.T) {
	calls := 0
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/shutdown-deliveries" || r.Header.Get("Authorization") != "Bearer cached-session" {
			t.Error("unbound delivery")
		}
		var batch struct {
			Deliveries []shutdownoutbox.Delivery `json:"deliveries"`
		}
		if json.NewDecoder(r.Body).Decode(&batch) != nil || len(batch.Deliveries) != 1 {
			t.Error("invalid batch")
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
	s := &Supervisor{shutdownQueue: q, process: process.New(appconfig.Config{}), client: control.New(server.URL, "bootstrap", func() string { return "cached-session" }), logger: log.New(io.Discard, "", 0)}
	s.reportShutdownDeliveries(context.Background())
	pending, _ := q.Pending()
	if len(pending) != 1 {
		t.Fatal("lost ACK deleted observation")
	}
	s.shutdownQueue = shutdownoutbox.New(root, "key", "scope")
	s.reportShutdownDeliveries(context.Background())
	pending, err := s.shutdownQueue.Pending()
	if err != nil || len(pending) != 0 || len(seen) != 1 || calls != 2 {
		t.Fatalf("replay failed: %d %d %v", len(pending), calls, err)
	}
}
func TestShutdownScopeProbeUsesCachedSessionWithoutForcedRegistration(t *testing.T) {
	for _, body := range []string{`{"success":true,"data":{"version":1,"instance_id":8,"owner_user_id":7}}`, `{"success":true,"data":{"version":1,"instance_id":9,"owner_user_id":7}}`, `{"success":false,"data":{"version":1,"instance_id":8,"owner_user_id":7}}`} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/api/v1/agent/shutdown-deliveries/scope" || r.Header.Get("Authorization") != "Bearer cached-session" {
				t.Error("not scoped to cached session")
			}
			_, _ = w.Write([]byte(body))
		}))
		st, err := store.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Update(func(value *store.State) { value.OwnerUserID = 99 })
		s := &Supervisor{store: st, cfg: appconfig.Config{InstanceID: "8"}, client: control.New(server.URL, "bootstrap", func() string { return "cached-session" }), logger: log.New(io.Discard, "", 0)}
		s.refreshShutdownDeliveryScope(context.Background())
		server.Close()
		want := 0
		if strings.Contains(body, `"success":true`) && strings.Contains(body, `"instance_id":8`) {
			want = 7
		}
		if st.Snapshot().OwnerUserID != want || calls != 1 {
			t.Fatal("stale scope retained or registration forced")
		}
	}
	q := shutdownoutbox.New(t.TempDir(), "key", "scope")
	s := &Supervisor{shutdownQueue: q}
	value := &process.ShutdownObservation{Generation: 2, ClaimID: strings.Repeat("a", 64), Outcome: "cleanup_unconfirmed"}
	if s.inlineShutdownObservation(value, 7) != nil || s.inlineShutdownObservation(value, 0) == nil {
		t.Fatal("inline generation/legacy compatibility")
	}
}
