package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

func TestIsolatedNetworkFailureRetainsCauseAndUnconfirmedWriter(t *testing.T) {
	exited := make(chan error, 1)
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 7, Done: exited, Stop: func(context.Context) error { return sandbox.ErrContainerResetRequired }, PrepareReset: func(context.Context, bool) error { return nil }}, nil
	}})
	created, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, created.GatewayID, "running")
	exited <- errors.Join(ErrNetworkAuthorizationLost, errors.New("secret process stderr"))
	awaitIsolatedState(t, m, created.GatewayID, "error")
	state := m.GatewayStates()[0]
	if state.FailureClass != "network_authorization_lost" || state.ErrorMessage != "network_authorization_lost" || state.Retryable == nil || *state.Retryable || len(m.ports.ListUsed()) != 1 {
		t.Fatalf("lost cause or released unconfirmed writer: %+v", state)
	}
}
