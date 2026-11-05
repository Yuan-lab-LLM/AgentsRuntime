package gateway

import (
	"context"
	"fmt"
	"testing"
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
