package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfirmedStopUsesIsolatedLifecycleOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	defer released.Do(func() { close(release) })
	var starts atomic.Int32
	m := isolatedTestManager(t, isolatedTestProfile{prepare: func(CreateGatewayRequest) error {
		close(entered)
		<-release
		return nil
	}}, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		starts.Add(1)
		return ManagedProcess{}, nil
	}})
	created, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- m.StopGatewayConfirmed(context.Background(), created.GatewayID) }()
	select {
	case err := <-stopped:
		t.Fatalf("confirmed stop returned before pending workspace writer stopped: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if len(m.ports.ListUsed()) != 1 {
		t.Fatal("port released before writer termination")
	}
	released.Do(func() { close(release) })
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 || len(m.ports.ListUsed()) != 0 || len(m.GatewayStates()) != 0 {
		t.Fatal("cancelled start survived confirmed stop")
	}
}
