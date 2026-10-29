package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recoveryProcessStarter struct {
	mu        sync.Mutex
	calls     int
	fail      bool
	nextPID   int
	processes []chan error
}

func (s *recoveryProcessStarter) StartGateway(context.Context, GatewayStartSpec) (ManagedProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return ManagedProcess{}, errors.New("forced restart failure")
	}
	s.nextPID++
	done := make(chan error, 1)
	s.processes = append(s.processes, done)
	return ManagedProcess{PID: s.nextPID, Done: done}, nil
}

func (s *recoveryProcessStarter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func waitForGatewayState(t *testing.T, manager *GatewayManager, id, state string) GatewayState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if current, ok := manager.GatewayState(id); ok && current.State == state {
			return current
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, _ := manager.GatewayState(id)
	t.Fatalf("gateway %s state = %+v, want %s", id, current, state)
	return GatewayState{}
}

func newRecoveryTestManager(starter ProcessStarter) *GatewayManager {
	manager := NewGatewayManager(Config{RuntimeType: "custom", Runtime: injectedPolicyProfile{}}, starter, NewPortAllocator(nil))
	manager.SetHealthChecker(noopGatewayHealthChecker{})
	manager.restartDelays = []time.Duration{0, 0, 0}
	return manager
}

func TestUnexpectedRunningGatewayExitRestartsInPlace(t *testing.T) {
	starter := &recoveryProcessStarter{nextPID: 40}
	manager := newRecoveryTestManager(starter)
	done := make(chan error, 1)
	id := "gw-17-3"
	manager.gateways[id] = &gatewayRecord{
		state:     GatewayState{GatewayID: id, InstanceID: 17, RuntimeType: "openclaw", Generation: 3, Port: 20003, PID: 39, State: "running"},
		process:   ManagedProcess{PID: 39, Done: done},
		startSpec: GatewayStartSpec{GatewayID: id, InstanceID: 17, RuntimeType: "openclaw", Generation: 3, Port: 20003},
		readyAt:   time.Now().UTC(),
	}
	go manager.watchGatewayProcess(id, 39, done)
	done <- errors.New("exit status 1")

	deadline := time.Now().Add(2 * time.Second)
	var state GatewayState
	for time.Now().Before(deadline) {
		state, _ = manager.GatewayState(id)
		if state.State == "running" && state.PID == 41 && starter.callCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state.PID != 41 || starter.callCount() != 1 {
		t.Fatalf("recovered state = %+v, restart calls = %d", state, starter.callCount())
	}
	if state.FailureClass != "" || state.Retryable != nil || state.RestartAttempt != 0 {
		t.Fatalf("successful recovery retained failure metadata: %+v", state)
	}
}

func TestUnexpectedGatewayExitStopsAfterBoundedRecovery(t *testing.T) {
	starter := &recoveryProcessStarter{fail: true}
	manager := newRecoveryTestManager(starter)
	manager.restartDelays = []time.Duration{0, 0}
	done := make(chan error, 1)
	id := "gw-18-4"
	manager.gateways[id] = &gatewayRecord{
		state:     GatewayState{GatewayID: id, InstanceID: 18, RuntimeType: "openclaw", Generation: 4, Port: 20006, PID: 51, State: "running"},
		process:   ManagedProcess{PID: 51, Done: done},
		startSpec: GatewayStartSpec{GatewayID: id, InstanceID: 18, RuntimeType: "openclaw", Generation: 4, Port: 20006},
		readyAt:   time.Now().UTC(),
	}
	go manager.watchGatewayProcess(id, 51, done)
	done <- errors.New("exit status 1")

	state := waitForGatewayState(t, manager, id, "error")
	if starter.callCount() != 2 || state.FailureClass != "unexpected_process_exit" || state.Retryable == nil || *state.Retryable || state.RestartAttempt != 2 {
		t.Fatalf("exhausted recovery state = %+v, restart calls = %d", state, starter.callCount())
	}
}

func TestLegacyOpenClawGatewayKeepsExistingExitBehavior(t *testing.T) {
	starter := &recoveryProcessStarter{nextPID: 70}
	manager := NewGatewayManager(Config{RuntimeType: "openclaw", OpenClawVersion: "2026.7.1"}, starter, NewPortAllocator(nil))
	manager.restartDelays = []time.Duration{0}
	done := make(chan error, 1)
	id := "gw-19-2"
	manager.gateways[id] = &gatewayRecord{
		state:   GatewayState{GatewayID: id, InstanceID: 19, RuntimeType: "openclaw", Generation: 2, Port: 20009, PID: 69, State: "running"},
		process: ManagedProcess{PID: 69, Done: done},
	}
	go manager.watchGatewayProcess(id, 69, done)
	done <- errors.New("exit status 1")

	state := waitForGatewayState(t, manager, id, "error")
	if starter.callCount() != 0 || state.RestartAttempt != 0 {
		t.Fatalf("legacy runtime behavior changed: state=%+v restart calls=%d", state, starter.callCount())
	}
}
