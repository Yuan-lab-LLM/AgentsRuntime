package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scopedAutomationTestProfile struct {
	isolatedTestProfile
	reconcile func(context.Context, int) error
}

func (scopedAutomationTestProfile) AutomaticGatewayRecovery(Config) bool { return true }
func (p scopedAutomationTestProfile) GatewayAutomations(Config, CreateGatewayRequest) func(context.Context, int) error {
	return p.reconcile
}

func TestIsolatedStopCancelsAndJoinsAutomationBeforeReleasingWriter(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	p := scopedAutomationTestProfile{reconcile: func(ctx context.Context, _ int) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}}
	var stopped atomic.Int32
	m := isolatedTestManager(t, p, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error { stopped.Add(1); return nil }}, nil
	}})
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	stopDone := make(chan error, 1)
	go func() { stopDone <- m.StopGatewayConfirmed(context.Background(), response.GatewayID) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel its in-flight automation")
	}
	select {
	case err := <-stopDone:
		t.Fatalf("Stop acknowledged before automation joined: %v", err)
	default:
	}
	m.mu.RLock()
	reserved := m.gateways[response.GatewayID].reserved
	m.mu.RUnlock()
	if !reserved {
		t.Fatal("writer reservation released while automation was still active")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after automation joined")
	}
	if stopped.Load() != 1 {
		t.Fatalf("gateway stops=%d", stopped.Load())
	}
}

func TestIsolatedRestartDrainsPreviousAttemptAutomation(t *testing.T) {
	entered, cancelled, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls, starts atomic.Int32
	p := scopedAutomationTestProfile{reconcile: func(ctx context.Context, _ int) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
		}
		return ctx.Err()
	}}
	died := make(chan error, 1)
	m := isolatedTestManager(t, p, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		n := starts.Add(1)
		proc := ManagedProcess{PID: int(n), Stop: func(context.Context) error { return nil }}
		if n == 1 {
			proc.Done = died
		} else {
			close(second)
		}
		return proc, nil
	}})
	m.restartDelays = []time.Duration{0}
	_, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	died <- fmt.Errorf("crashed")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("process exit did not cancel its own automation attempt")
	}
	select {
	case <-second:
		t.Fatal("successor started while old automation was active")
	default:
	}
	once.Do(func() { close(release) })
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("gateway did not recover after automation drained")
	}
}

func TestIsolatedUnconfirmedAutomationTeardownQuarantinesWriter(t *testing.T) {
	entered := make(chan struct{})
	var enteredOnce sync.Once
	p := scopedAutomationTestProfile{reconcile: func(context.Context, int) error {
		enteredOnce.Do(func() { close(entered) })
		return fmt.Errorf("auxiliary namespace teardown: %w", ErrGatewayStopFailed)
	}}
	var starts atomic.Int32
	m := isolatedTestManager(t, p, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		starts.Add(1)
		return ManagedProcess{PID: 123, Stop: func(context.Context) error { return nil }}, nil
	}})
	m.restartDelays = []time.Duration{0}
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	awaitIsolatedState(t, m, response.GatewayID, "error")
	if err := m.StopGatewayConfirmed(context.Background(), response.GatewayID); !errors.Is(err, ErrGatewayStopFailed) {
		t.Fatalf("unconfirmed automation allowed Stop: %v", err)
	}
	m.mu.RLock()
	record := m.gateways[response.GatewayID]
	reserved := record != nil && record.reserved
	m.mu.RUnlock()
	if !reserved || starts.Load() != 1 {
		t.Fatalf("uncertain writer released/restarted: reserved=%v starts=%d", reserved, starts.Load())
	}
}

type managedAutomationTestProfile struct{ isolatedTestProfile }

func (managedAutomationTestProfile) ManagedGatewayAutomations(_ Config, _ CreateGatewayRequest, execute GatewayAutomationExecutor) func(context.Context, int) error {
	return func(ctx context.Context, _ int) error {
		_, err := execute(ctx, []string{"native", "automations", "list"}, []string{"BUSINESS_AUTH=correct"})
		return err
	}
}

type managedAutomationTestStarter struct {
	isolatedTestStarter
	run func(context.Context, GatewayStartSpec) ([]byte, error)
}

func (s managedAutomationTestStarter) RunGatewayCommand(ctx context.Context, spec GatewayStartSpec) ([]byte, error) {
	return s.run(ctx, spec)
}

func TestManagedAutomationVerifiesListenerBeforeSendingCredentials(t *testing.T) {
	var commands, verified int
	starter := managedAutomationTestStarter{run: func(_ context.Context, spec GatewayStartSpec) ([]byte, error) {
		commands++
		if verified != 1 || spec.UID != 200007 || spec.GID != 200008 || spec.Generation != 2 || EnvironmentValue(spec.Env, "HOME") != "/assigned/home" || EnvironmentValue(spec.Env, "BUSINESS_AUTH") != "correct" || len(spec.Command) != 3 || spec.Command[0] != "native" {
			t.Fatalf("invalid business command: %+v verified=%d", spec, verified)
		}
		return []byte(`{}`), nil
	}}
	m := NewGatewayManager(Config{Runtime: managedAutomationTestProfile{}}, starter, nil)
	spec := GatewayStartSpec{UID: 200007, GID: 200008, Generation: 2, Env: []string{"HOME=/assigned/home", "BUSINESS_AUTH=stale"}}
	denied := errors.New("wrong listener namespace")
	process := ManagedProcess{VerifyListeners: func(context.Context) error { verified++; return denied }}
	hook := m.gatewayAutomationsForAttempt(CreateGatewayRequest{}, spec, process)
	if err := hook(context.Background(), 20000); !errors.Is(err, denied) || commands != 0 {
		t.Fatalf("credentials sent to unowned listener: %v commands=%d", err, commands)
	}
	verified = 0
	process.VerifyListeners = func(context.Context) error { verified++; return nil }
	hook = m.gatewayAutomationsForAttempt(CreateGatewayRequest{}, spec, process)
	if err := hook(context.Background(), 20000); err != nil || commands != 1 {
		t.Fatalf("owned command failed: %v commands=%d", err, commands)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hook(ctx, 20000); !errors.Is(err, context.Canceled) || commands != 1 {
		t.Fatalf("cancelled command launched: %v", err)
	}
	process.VerifyListeners = nil
	hook = m.gatewayAutomationsForAttempt(CreateGatewayRequest{}, spec, process)
	if err := hook(context.Background(), 20000); err == nil || commands != 1 {
		t.Fatal("missing ownership verifier allowed automation")
	}
}

func TestIsolatedCancellationDuringAutomationRetryDelayStopsPromptly(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	p := scopedAutomationTestProfile{reconcile: func(context.Context, int) error {
		if calls.Add(1) == 1 {
			close(entered)
		}
		return errors.New("transient failure")
	}}
	m := isolatedTestManager(t, p, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error { return nil }}, nil
	}})
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := m.StopGatewayConfirmed(ctx, response.GatewayID); err != nil {
		t.Fatalf("retry sleep blocked Stop: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("automation retried after cancellation: %d", calls.Load())
	}
}

func TestIsolatedStopRetainsWriterWhenAutomationCleanupIsUnconfirmed(t *testing.T) {
	entered := make(chan struct{})
	p := scopedAutomationTestProfile{reconcile: func(ctx context.Context, _ int) error {
		close(entered)
		<-ctx.Done()
		return fmt.Errorf("unknown auxiliary namespace: %w", ErrGatewayStopFailed)
	}}
	m := isolatedTestManager(t, p, isolatedTestStarter{start: func(GatewayStartSpec) (ManagedProcess, error) {
		return ManagedProcess{PID: 123, Stop: func(context.Context) error { return nil }}, nil
	}})
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := m.StopGatewayConfirmed(context.Background(), response.GatewayID); !errors.Is(err, ErrGatewayStopFailed) {
		t.Fatalf("stop acknowledged unknown auxiliary cleanup: %v", err)
	}
	m.mu.RLock()
	record := m.gateways[response.GatewayID]
	held := record != nil && record.reserved && record.automationStopFailed
	m.mu.RUnlock()
	if !held {
		t.Fatal("Stop released uncertain writer")
	}
	// Retrying main-process Stop is insufficient evidence about a different
	// auxiliary namespace; the writer must remain quarantined.
	if err := m.StopGatewayConfirmed(context.Background(), response.GatewayID); !errors.Is(err, ErrGatewayStopFailed) {
		t.Fatalf("stop retry lost auxiliary quarantine: %v", err)
	}
}
