package agent

import (
	"testing"
	"time"
)

func TestHeartbeatPhaseOffsetIsStableAndBounded(t *testing.T) {
	interval := 2 * time.Second
	first := heartbeatPhaseOffset("pod-uid-1", "pod-a", interval)
	second := heartbeatPhaseOffset("pod-uid-1", "pod-b", interval)
	if first != second {
		t.Fatalf("phase offset changed for the same pod UID: %s != %s", first, second)
	}
	if first < 0 || first >= 500*time.Millisecond {
		t.Fatalf("phase offset %s is outside the expected window", first)
	}
}
