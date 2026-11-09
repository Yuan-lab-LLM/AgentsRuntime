package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsolatedClockBlocksReadinessOnLargeSkew(t *testing.T) {
	for _, tc := range []struct {
		name      string
		offset    time.Duration
		wantError bool
	}{{"in sync", 0, false}, {"warning", 45 * time.Second, false}, {"blocked", 180 * time.Second, true}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.Header.Get(AgentTokenHeader) != "" {
					t.Error("clock request should be credential-free HEAD")
				}
				w.Header().Set("Date", time.Now().Add(tc.offset).UTC().Format(http.TimeFormat))
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{})
			m.cfg.BackendURL = server.URL
			err := m.RefreshClock(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("clock error=%v", err)
			}
			if tc.wantError && (m.RegisterPayload().State == "ready" || m.HeartbeatPayload(1).State == "ready" || m.Health() == nil) {
				t.Fatal("large skew reported ready")
			}
		})
	}
}

func TestIsolatedClockPreservesRecentSampleThroughTransientOutage(t *testing.T) {
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{})
	if err := m.recordClockIssue(""); err != nil {
		t.Fatal(err)
	}
	if err := m.recordClockIssue("clock_reference_unavailable"); err != nil {
		t.Fatal("transient reference outage invalidated fresh sample")
	}
	m.mu.Lock()
	m.clockLastGood = time.Now().Add(-6 * time.Minute)
	m.mu.Unlock()
	if err := m.recordClockIssue("clock_reference_unavailable"); err == nil {
		t.Fatal("stale reference sample accepted")
	}
}
