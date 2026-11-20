package slots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestSlotStateRespondsWhileStorageProbeIsBlocked(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var attempts atomic.Int32
	health := &slotStorageHealth{probe: func() error {
		if attempts.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}}
	cfg := gateway.Config{
		PodUID: "pod", ControlToken: "control", Capacity: 1,
		WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(),
		GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1,
		ManagementStorageHealth: health.health,
	}
	w, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	probeDone := make(chan struct{})
	go func() { defer close(probeDone); health.run(ctx, time.Millisecond) }()
	<-started
	var releaseOnce sync.Once
	stopBlocking := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { cancel(); stopBlocking(); <-probeDone }()
	var pending sync.WaitGroup
	state := func() (*httptest.ResponseRecorder, Snapshot) {
		r := httptest.NewRequest("GET", "/v1/slot-state", nil)
		r.Header.Set(gateway.ControlTokenHeader, "control")
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		var snapshot Snapshot
		_ = json.Unmarshal(out.Body.Bytes(), &snapshot)
		return out, snapshot
	}
	for i := 0; i < 2; i++ {
		pending.Add(1)
		go func() {
			defer pending.Done()
			out, snapshot := state()
			if out.Code != 200 || !snapshot.Quarantined || !snapshot.StorageUnavailable || snapshot.IdleConfirmed {
				t.Errorf("expected a conservative snapshot, got %d %s", out.Code, out.Body.String())
			}
		}()
	}
	done := make(chan struct{})
	go func() { pending.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("slot state requests wait for storage I/O")
	}
	if attempts.Load() != 1 {
		t.Fatal("polling started additional probes")
	}
	stopBlocking()
	deadline := time.Now().Add(time.Second)
	for health.health() != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	out, snapshot := state()
	if out.Code != 200 || snapshot.Quarantined || !snapshot.IdleConfirmed {
		t.Fatalf("slot did not recover after storage responded: %d %s", out.Code, out.Body.String())
	}
	cancel()
	<-probeDone
	if health.health() == nil {
		t.Fatal("cancelled sampler still reports healthy")
	}
}

func TestSlotStorageHealthRejectsExpiredAndOverdueSamples(t *testing.T) {
	h := &slotStorageHealth{}
	now := time.Now()
	h.sample.Store(&storageHealthSample{verified: now.Add(-storageProbeFreshness)})
	if h.health() == nil {
		t.Fatal("expired storage sample accepted")
	}
	h.sample.Store(&storageHealthSample{verified: now, pending: now.Add(-storageProbeBudget)})
	if h.health() == nil {
		t.Fatal("overdue storage probe accepted")
	}
}

func TestStandbyRegistrationWaitsForInitialStorageEvidence(t *testing.T) {
	sample := Snapshot{Quarantined: true, StorageUnavailable: true}
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	pool, err := NewPool(cfg, 1, func(_ context.Context, _ int, _, _ string, _ []byte) (int, []byte, error) {
		data, err := json.Marshal(sample)
		return http.StatusOK, data, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if pool.standbySlotStorageReady(t.Context()) {
		t.Fatal("upgrade standby advertised before first storage sample")
	}
	sample.StorageUnavailable = false
	sample.MaintenanceOperationID = "previous-operation"
	if !pool.standbySlotStorageReady(t.Context()) {
		t.Fatal("existing maintenance could not register for reconciliation")
	}
}

func TestPoolWorkbenchReturnsTemporaryFailureForTimedOutSlot(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	pool, err := NewPool(cfg, 1, func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if path != "/v1/slot-state" {
			t.Error("unverified request forwarded")
		}
		<-ctx.Done()
		return 0, nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/workbench", strings.NewReader(`{"instance_id":1,"user_id":2,"generation":3,"gateway_id":"gw-1-3","operation":"capabilities"}`))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	out := httptest.NewRecorder()
	start := time.Now()
	pool.ServeHTTP(out, r)
	if out.Code != http.StatusServiceUnavailable || !strings.Contains(out.Body.String(), "slot_state_unavailable") || time.Since(start) > 4*time.Second {
		t.Fatalf("unexpected slot timeout: %d %s", out.Code, out.Body.String())
	}
}
