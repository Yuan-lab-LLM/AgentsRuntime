package slots

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestContainerPrimaryGracefulTimeoutNeverKills(t *testing.T) {
	var mu sync.Mutex
	waited := make(chan struct{})
	signals, kills := 0, 0
	stop := newContainerPrimaryStop(&mu, func(context.Context) error { signals++; return nil }, func() error { kills++; return nil }, waited, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := stop(ctx, true); err == nil {
		t.Fatal("timeout claimed graceful application completion")
	}
	if signals != 1 || kills != 0 {
		t.Fatalf("graceful shutdown escalated: signals=%d kills=%d", signals, kills)
	}
	close(waited)
	if err := stop(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if signals != 1 || kills != 0 {
		t.Fatal("retry signaled a potentially recycled primary")
	}
}
