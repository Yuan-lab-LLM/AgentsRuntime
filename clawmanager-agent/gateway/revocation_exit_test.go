package gateway

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRevocationExitHelper(t *testing.T) {
	if os.Getenv("CM_REVOCATION_EXIT_HELPER") == "1" {
		os.Exit(78)
	}
}

func revocationTestExit(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRevocationExitHelper$")
	cmd.Env = append(os.Environ(), "CM_REVOCATION_EXIT_HELPER=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper did not exit")
	}
	return err
}

func TestRevocationExitScope(t *testing.T) {
	err := revocationTestExit(t)
	env := []string{"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE=1", "CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL=1"}
	for _, tc := range []struct {
		name, runtime string
		env           []string
		want          bool
	}{
		{"enabled", "hermes", env, true}, {"legacy", "hermes", nil, false},
		{"other runtime", "openclaw", env, false},
		{"Harness enabled", "deepseek-harness", []string{"CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE=1", "CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL=1"}, true},
		{"Harness legacy", "deepseek-harness", nil, false},
		{"Harness Team", "deepseek-harness", []string{"CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE=1", "CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL=1", "CLAWMANAGER_TEAM_ENABLED=true"}, false},
		{"team", "hermes", append(append([]string{}, env...), "CLAWMANAGER_TEAM_ENABLED=true"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := revocationExitRequiresCleanup(GatewayStartSpec{RuntimeType: tc.runtime, Env: tc.env}, err); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	if revocationExitRequiresCleanup(GatewayStartSpec{RuntimeType: "hermes", Env: env}, errors.New("exit status 78")) {
		t.Fatal("text error used as exit evidence")
	}
}

func TestRevocationCleanupFailureRetainsBindingAndBlocksRestart(t *testing.T) {
	starter := &recoveryProcessStarter{nextPID: 40}
	manager := newRecoveryTestManager(starter)
	done := make(chan error, 1)
	id := "gw-17-3"
	manager.gateways[id] = &gatewayRecord{state: GatewayState{GatewayID: id, InstanceID: 17, Generation: 3, Port: 20003, PID: 39, State: "running"}, process: ManagedProcess{PID: 39, Done: done}, readyAt: time.Now()}
	done <- errors.Join(revocationTestExit(t), ErrGatewayStopFailed)
	manager.watchGatewayProcess(id, 39, done)
	state, _ := manager.GatewayState(id)
	if state.State != "stop_error" || state.PID != 39 || state.Port != 20003 || state.ShutdownAudit != "cleanup_unconfirmed" || starter.callCount() != 0 {
		t.Fatalf("unsafe cleanup state: %+v", state)
	}
	if state.Retryable == nil || *state.Retryable {
		t.Fatal("cleanup failure became retryable")
	}
}

func TestHermesRevocationExitCanRecoverInPlaceAfterCleanup(t *testing.T) {
	starter := &recoveryProcessStarter{nextPID: 40}
	manager := newRecoveryTestManager(starter)
	manager.cfg.RuntimeType = "hermes"
	done := make(chan error, 1)
	id := "gw-hermes-17-3"
	spec := GatewayStartSpec{GatewayID: id, RuntimeType: "hermes", InstanceID: 17, Generation: 3, Port: 20003,
		Env: []string{"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE=1", "CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL=1"}}
	manager.gateways[id] = &gatewayRecord{
		state:   GatewayState{GatewayID: id, RuntimeType: "hermes", InstanceID: 17, Generation: 3, Port: 20003, PID: 39, State: "running"},
		process: ManagedProcess{PID: 39, Done: done}, startSpec: spec, readyAt: time.Now(),
	}
	go manager.watchGatewayProcess(id, 39, done)
	done <- revocationTestExit(t) // Cleanup errors would be wrapped before notification.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := manager.GatewayState(id)
		if state.State == "running" && state.PID == 41 {
			if state.Port != 20003 || starter.callCount() != 1 {
				t.Fatalf("recovery changed placement: %+v", state)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, _ := manager.GatewayState(id)
	t.Fatalf("Hermes did not recover in place: %+v", state)
}
