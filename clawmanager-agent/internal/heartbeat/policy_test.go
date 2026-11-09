package heartbeat

import (
	"errors"
	"testing"
	"time"
)

func TestNextDelayStaysWithinTwentyFivePercent(t *testing.T) {
	for range 1000 {
		got := NextDelay(DefaultInterval)
		if got < 90*time.Second || got > 150*time.Second {
			t.Fatalf("NextDelay(%s) = %s, want 90s..150s", DefaultInterval, got)
		}
	}
}

func TestNextDelayPreservesNonPositiveDuration(t *testing.T) {
	for _, value := range []time.Duration{0, -time.Second} {
		if got := NextDelay(value); got != value {
			t.Fatalf("NextDelay(%s) = %s, want unchanged", value, got)
		}
	}
}

func TestFailureTrackerRequiresConsecutiveFailuresAndRecovers(t *testing.T) {
	tracker := NewFailureTracker(DefaultFailureThreshold)
	failure := errors.New("unavailable")

	first := tracker.Observe(failure)
	if first.ConsecutiveFailures != 1 || first.Unavailable || first.BecameUnavailable {
		t.Fatalf("first failure = %+v", first)
	}

	reset := tracker.Observe(nil)
	if reset.Recovered || reset.Unavailable || reset.ConsecutiveFailures != 0 {
		t.Fatalf("success before threshold = %+v", reset)
	}

	tracker.Observe(failure)
	second := tracker.Observe(failure)
	if second.ConsecutiveFailures != 2 || !second.Unavailable || !second.BecameUnavailable {
		t.Fatalf("second consecutive failure = %+v", second)
	}

	repeated := tracker.Observe(failure)
	if repeated.ConsecutiveFailures != 3 || !repeated.Unavailable || repeated.BecameUnavailable {
		t.Fatalf("repeated failure = %+v", repeated)
	}

	recovered := tracker.Observe(nil)
	if !recovered.Recovered || recovered.Unavailable || recovered.ConsecutiveFailures != 0 {
		t.Fatalf("recovery = %+v", recovered)
	}
}
