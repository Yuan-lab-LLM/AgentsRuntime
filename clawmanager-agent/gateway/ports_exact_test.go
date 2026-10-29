package gateway

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestPortAllocatorReservesExactPortAndReleasesItsBlock(t *testing.T) {
	alloc := NewPortAllocator(func(int) bool { return false })
	alloc.SetBlockSize(3)

	port, err := alloc.ReserveExact(101, 7, 20003)
	if err != nil || port != 20003 {
		t.Fatalf("ReserveExact() = %d, %v; want 20003, nil", port, err)
	}
	if _, err := alloc.ReserveExact(102, 7, 20004); !errors.Is(err, ErrNoFreePort) || !strings.Contains(err.Error(), "20004") {
		t.Fatalf("overlapping ReserveExact() error = %v, want diagnostic ErrNoFreePort", err)
	}
	if _, err := alloc.ReserveExact(102, 7, 20001); !errors.Is(err, ErrNoFreePort) || !strings.Contains(err.Error(), "20003") || !strings.Contains(err.Error(), "reserved by AgentRuntime") {
		t.Fatalf("overlapping ReserveExact() error = %v, want diagnostic with reserved block member 20003", err)
	}

	alloc.Release(20003)
	port, err = alloc.ReserveExact(102, 7, 20003)
	if err != nil || port != 20003 {
		t.Fatalf("ReserveExact() after release = %d, %v; want 20003, nil", port, err)
	}
}

func TestPortAllocatorReserveExactReportsListeningBlockMember(t *testing.T) {
	for _, listeningPort := range []int{20051, 20052, 20053} {
		t.Run(strconv.Itoa(listeningPort), func(t *testing.T) {
			alloc := NewPortAllocator(func(port int) bool { return port == listeningPort })
			alloc.SetBlockSize(3)

			_, err := alloc.ReserveExact(415, 1, 20051)
			if !errors.Is(err, ErrNoFreePort) {
				t.Fatalf("ReserveExact() error = %v, want ErrNoFreePort", err)
			}
			if !strings.Contains(err.Error(), "requested gateway port 20051") {
				t.Fatalf("ReserveExact() error = %q, want requested gateway port", err)
			}
			if !strings.Contains(err.Error(), "port block member "+strconv.Itoa(listeningPort)) {
				t.Fatalf("ReserveExact() error = %q, want conflict member %d", err, listeningPort)
			}
			if !strings.Contains(err.Error(), "already listening") {
				t.Fatalf("ReserveExact() error = %q, want listening reason", err)
			}
		})
	}
}
