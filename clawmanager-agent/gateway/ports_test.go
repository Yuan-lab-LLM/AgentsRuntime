package gateway

import (
	"sync"
	"testing"
)

func TestPortAllocatorReservesCommitsAndReleasesPorts(t *testing.T) {
	alloc := NewPortAllocator(func(int) bool { return false })
	alloc.SetBlockSize(1)
	rng := PortRange{Start: 31000, End: 31001}

	first, err := alloc.Reserve(101, 7, rng)
	if err != nil {
		t.Fatalf("Reserve first error = %v", err)
	}
	alloc.Commit(101, 7, first)

	second, err := alloc.Reserve(102, 7, rng)
	if err != nil {
		t.Fatalf("Reserve second error = %v", err)
	}
	if first == second {
		t.Fatalf("second reserve reused committed port %d", first)
	}
	alloc.Commit(102, 7, second)

	if _, err := alloc.Reserve(103, 7, rng); err != ErrNoFreePort {
		t.Fatalf("Reserve exhausted error = %v, want ErrNoFreePort", err)
	}

	alloc.Release(first)
	reused, err := alloc.Reserve(104, 7, rng)
	if err != nil {
		t.Fatalf("Reserve after release error = %v", err)
	}
	if reused != first {
		t.Fatalf("Reserve after release = %d, want released port %d", reused, first)
	}
}

func TestPortAllocatorSkipsSystemListeningPorts(t *testing.T) {
	alloc := NewPortAllocator(func(port int) bool { return port == 32000 })
	alloc.SetBlockSize(1)

	port, err := alloc.Reserve(101, 7, PortRange{Start: 32000, End: 32001})
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if port != 32001 {
		t.Fatalf("Reserve = %d, want first non-listening port 32001", port)
	}
}

func TestPortAllocatorReservesContiguousPortBlocks(t *testing.T) {
	alloc := NewPortAllocator(func(int) bool { return false })
	alloc.SetBlockSize(3)
	rng := PortRange{Start: 31000, End: 31006}

	first, err := alloc.Reserve(101, 7, rng)
	if err != nil {
		t.Fatalf("Reserve first error = %v", err)
	}
	if first != 31000 {
		t.Fatalf("Reserve first = %d, want 31000", first)
	}
	alloc.Commit(101, 7, first)

	second, err := alloc.Reserve(102, 7, rng)
	if err != nil {
		t.Fatalf("Reserve second error = %v", err)
	}
	if second != 31003 {
		t.Fatalf("Reserve second = %d, want next block primary 31003", second)
	}
	alloc.Commit(102, 7, second)

	if _, err := alloc.Reserve(103, 7, rng); err != ErrNoFreePort {
		t.Fatalf("Reserve exhausted error = %v, want ErrNoFreePort", err)
	}

	alloc.Release(first)
	reused, err := alloc.Reserve(104, 7, rng)
	if err != nil {
		t.Fatalf("Reserve after release error = %v", err)
	}
	if reused != first {
		t.Fatalf("Reserve after release = %d, want released block primary %d", reused, first)
	}
}

func TestPortAllocatorSkipsListeningPortsInsidePortBlock(t *testing.T) {
	alloc := NewPortAllocator(func(port int) bool { return port == 32001 })
	alloc.SetBlockSize(3)

	port, err := alloc.Reserve(101, 7, PortRange{Start: 32000, End: 32005})
	if err != nil {
		t.Fatalf("Reserve error = %v", err)
	}
	if port != 32003 {
		t.Fatalf("Reserve = %d, want first block without listening sidecar port 32003", port)
	}
}

func TestPortAllocatorConcurrentReserveReturnsUniquePortBlocks(t *testing.T) {
	alloc := NewPortAllocator(func(int) bool { return false })
	alloc.SetBlockSize(3)
	rng := PortRange{Start: 40000, End: 40299}

	const workers = 100
	var wg sync.WaitGroup
	results := make(chan int, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			port, err := alloc.Reserve(1000+index, 7, rng)
			if err != nil {
				errs <- err
				return
			}
			results <- port
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("Reserve() error = %v", err)
	}
	seen := map[int]bool{}
	for port := range results {
		if seen[port] {
			t.Fatalf("duplicate primary port reserved: %d", port)
		}
		seen[port] = true
	}
	if len(seen) != workers {
		t.Fatalf("reserved %d unique ports, want %d", len(seen), workers)
	}
	if used := len(alloc.ListUsed()); used != workers*3 {
		t.Fatalf("reserved block members = %d, want %d", used, workers*3)
	}
}
