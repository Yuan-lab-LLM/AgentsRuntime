package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

func TestContainerStartupFailurePreservesCauseAndWriterFence(t *testing.T) {
	m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, PrepareReset: func(context.Context, bool) error { return nil }, Stop: func(context.Context) error { return fmt.Errorf("container reset proof required") }}, fmt.Errorf("http_health_failed: diagnostic startup failure")
	}})
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "error")
	m.mu.RLock()
	record := m.gateways[response.GatewayID]
	cause, reserved, pid := record.state.ErrorMessage, record.reserved, record.process.PID
	m.mu.RUnlock()
	if cause != "http_health_failed" {
		t.Fatalf("required container reset masked native startup cause: %s", cause)
	}
	if !reserved || pid != 123 {
		t.Fatal("diagnostic startup error released unconfirmed writer")
	}
}

func TestContainerOnlyResetRequirementIsPendingNotGenericStopFailure(t *testing.T) {
	for _, realFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(realFailure), func(t *testing.T) {
			m := isolatedTestManager(t, isolatedTestProfile{}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
				return ManagedProcess{PID: 123, PrepareReset: func(context.Context, bool) error { return nil }, Stop: func(context.Context) error {
					if realFailure {
						return errors.Join(context.DeadlineExceeded, sandbox.ErrContainerResetRequired)
					}
					return errors.Join(nil, sandbox.ErrContainerResetRequired)
				}}, nil
			}})
			response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
			if err != nil {
				t.Fatal(err)
			}
			awaitIsolatedState(t, m, response.GatewayID, "running")
			m.mu.Lock()
			record := m.gateways[response.GatewayID]
			record.cancel()
			m.mu.Unlock()
			awaitIsolatedState(t, m, response.GatewayID, "error")
			m.mu.RLock()
			cause, reserved, pid := record.state.ErrorMessage, record.reserved, record.process.PID
			m.mu.RUnlock()
			want := "container_reset_required"
			if realFailure {
				want = "gateway_stop_failed"
			}
			if cause != want {
				t.Fatalf("cause=%q want=%q", cause, want)
			}
			if !reserved || pid != 123 {
				t.Fatal("pending reset released the old writer before container termination")
			}
		})
	}
}
