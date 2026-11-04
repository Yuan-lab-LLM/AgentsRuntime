package slots

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSandboxGracefulStopTimeoutRetriesWithoutResignaling(t *testing.T) {
	waited := make(chan struct{})
	signals, proofs := 0, 0
	var mu sync.Mutex
	stop := newSandboxGracefulStop(&mu, func(context.Context) error { signals++; return nil }, func(context.Context) error { proofs++; return nil }, waited)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	close(waited)
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if signals != 1 || proofs != 1 {
		t.Fatalf("signals=%d proofs=%d", signals, proofs)
	}
}

func TestSandboxGracefulStopRequiresNamespaceProofAndPinnedSignal(t *testing.T) {
	for _, signalFailure := range []bool{false, true} {
		waited := make(chan struct{})
		close(waited)
		var mu sync.Mutex
		stop := newSandboxGracefulStop(&mu, func(context.Context) error { t.Fatal("already reaped leader was signaled"); return nil }, func(context.Context) error { return errors.New("namespace survives") }, waited)
		if signalFailure {
			waited = make(chan struct{})
			stop = newSandboxGracefulStop(&mu, func(context.Context) error { return errors.New("pidfd unavailable") }, func(context.Context) error { t.Fatal("failed identity became confirmation"); return nil }, waited)
		}
		if err := stop(context.Background()); err == nil {
			t.Fatal("unproven namespace accepted")
		}
	}
}

func TestSandboxIdlePolicyFencesNetworkForceKillAtomically(t *testing.T) {
	policy := &sandboxStopPolicy{}
	kills := 0
	if err := policy.fenceIdle(func() error { return errors.New("persist failed") }); err == nil {
		t.Fatal("failed durable fence accepted")
	}
	policy.networkFailure(func() { kills++ })
	if kills != 1 {
		t.Fatal("ordinary failure policy changed")
	}
	if err := policy.fenceIdle(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	policy.networkFailure(func() { kills++ })
	if kills != 1 {
		t.Fatal("network failure force-killed idle fenced namespace")
	}
}
