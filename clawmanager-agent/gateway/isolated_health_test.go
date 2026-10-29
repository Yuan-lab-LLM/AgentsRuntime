package gateway

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type isolatedScriptedHealthChecker struct {
	results chan error
}

func (c *isolatedScriptedHealthChecker) WaitReady(context.Context, GatewayStartSpec) error {
	return nil
}

func (c *isolatedScriptedHealthChecker) CheckHealth(ctx context.Context, _ GatewayStartSpec) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-c.results:
		return result
	}
}

func TestIsolatedTransientHealthFailuresDebounceAndRecover(t *testing.T) {
	var stops atomic.Int32
	health := &isolatedScriptedHealthChecker{results: make(chan error)}
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		}}, nil
	}})
	m.SetHealthChecker(health)
	m.nextGatewayHealthCheckDelay = func() time.Duration { return time.Millisecond }

	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "running")

	health.results <- fmt.Errorf("dashboard_http_unavailable")
	time.Sleep(10 * time.Millisecond)
	state := m.GatewayStates()[0]
	if state.State != "running" || state.ErrorMessage != "" {
		t.Fatalf("first transient failure changed state: %+v", state)
	}

	health.results <- fmt.Errorf("dashboard_http_unavailable")
	awaitIsolatedState(t, m, response.GatewayID, "unhealthy")
	state = m.GatewayStates()[0]
	if state.ErrorMessage != "dashboard_http_unavailable" || stops.Load() != 0 {
		t.Fatalf("debounced failure state = %+v, stops = %d", state, stops.Load())
	}

	health.results <- nil
	awaitIsolatedState(t, m, response.GatewayID, "running")
	state = m.GatewayStates()[0]
	if state.ErrorMessage != "" || stops.Load() != 0 {
		t.Fatalf("recovered state = %+v, stops = %d", state, stops.Load())
	}
}

func TestIsolatedHealthFailuresUseCommonDebouncePolicy(t *testing.T) {
	var stops atomic.Int32
	health := &isolatedScriptedHealthChecker{results: make(chan error)}
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		}}, nil
	}})
	m.SetHealthChecker(health)
	m.nextGatewayHealthCheckDelay = func() time.Duration { return time.Millisecond }

	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "running")
	health.results <- fmt.Errorf("dashboard_auth_failed")
	time.Sleep(10 * time.Millisecond)
	state := m.GatewayStates()[0]
	if state.State != "running" || stops.Load() != 0 {
		t.Fatalf("first health failure state = %+v, stops = %d", state, stops.Load())
	}
	health.results <- fmt.Errorf("dashboard_auth_failed")
	awaitIsolatedState(t, m, response.GatewayID, "unhealthy")
	state = m.GatewayStates()[0]
	if state.ErrorMessage != "dashboard_auth_failed" || stops.Load() != 0 {
		t.Fatalf("debounced failure state = %+v, stops = %d", state, stops.Load())
	}
}
