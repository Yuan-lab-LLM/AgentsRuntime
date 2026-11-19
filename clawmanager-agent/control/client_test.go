package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestReportHeartbeatUsesIndependentShortTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(75 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewReportClient(gateway.Config{BackendURL: server.URL, HeartbeatInterval: 40 * time.Millisecond})
	client.httpClient.Timeout = 250 * time.Millisecond
	client.heartbeatClient.Timeout = 20 * time.Millisecond

	started := time.Now()
	if err := client.ReportHeartbeat(context.Background(), gateway.HeartbeatPayload{}); err == nil {
		t.Fatal("ReportHeartbeat returned nil error after its dedicated timeout")
	}
	if elapsed := time.Since(started); elapsed >= 70*time.Millisecond {
		t.Fatalf("heartbeat elapsed %s, want it to fail before the shared report completes", elapsed)
	}
	if err := client.ReportMetrics(context.Background(), gateway.MetricsPayload{}); err != nil {
		t.Fatalf("ReportMetrics used the heartbeat timeout: %v", err)
	}
}

func TestHeartbeatRequestTimeoutStaysBelowInterval(t *testing.T) {
	for _, test := range []struct {
		interval time.Duration
		want     time.Duration
	}{
		{interval: 2 * time.Second, want: time.Second},
		{interval: 400 * time.Millisecond, want: 200 * time.Millisecond},
		{interval: 5 * time.Second, want: time.Second},
		{interval: 0, want: time.Second},
	} {
		if got := heartbeatRequestTimeout(test.interval); got != test.want {
			t.Fatalf("heartbeatRequestTimeout(%s) = %s, want %s", test.interval, got, test.want)
		}
	}
}
