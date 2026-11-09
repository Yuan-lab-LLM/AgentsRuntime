package gateway

import (
	"errors"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/heartbeat"
)

func TestGatewayHealthDelayUsesSharedConfiguredInterval(t *testing.T) {
	m := NewGatewayManager(Config{GatewayReportInterval: 80 * time.Second}, isolatedTestStarter{}, nil)
	for range 100 {
		delay := m.nextGatewayHealthDelay()
		if delay < 60*time.Second || delay > 100*time.Second {
			t.Fatalf("health delay = %s, want 60s..100s", delay)
		}
	}
}

func TestGatewayHealthObservationUsesCommonHeartbeatPolicy(t *testing.T) {
	const gatewayID = "gw-7-3"
	m := &GatewayManager{
		gateways: map[string]*gatewayRecord{
			gatewayID: {state: GatewayState{GatewayID: gatewayID, InstanceID: 7, Generation: 3, State: "running"}, process: ManagedProcess{PID: 12}, reserved: true},
		},
		changes: make(chan struct{}, 1),
	}
	failures := heartbeat.NewFailureTracker(heartbeat.DefaultFailureThreshold)
	probeErr := errors.New("generic runtime probe failed")

	if !m.observeGatewayHealth(gatewayID, 7, 3, 12, &failures, probeErr) {
		t.Fatal("first observation lost the gateway")
	}
	if got := m.gateways[gatewayID].state.State; got != "running" {
		t.Fatalf("state after first failure = %q, want running", got)
	}

	if !m.observeGatewayHealth(gatewayID, 7, 3, 12, &failures, probeErr) {
		t.Fatal("second observation lost the gateway")
	}
	state := m.gateways[gatewayID].state
	if state.State != "unhealthy" || state.ErrorMessage != "gateway_health_unavailable" {
		t.Fatalf("state after second failure = %+v", state)
	}
	if got := m.usedSlotsLocked(); got != 1 {
		t.Fatalf("used slots while unhealthy = %d, want 1", got)
	}

	if !m.observeGatewayHealth(gatewayID, 7, 3, 12, &failures, nil) {
		t.Fatal("recovery observation lost the gateway")
	}
	state = m.gateways[gatewayID].state
	if state.State != "running" || state.ErrorMessage != "" || state.HealthAt.IsZero() {
		t.Fatalf("recovered state = %+v", state)
	}
}
