// Package heartbeat defines the reporting and failure policy shared by every
// clawmanager-agent operating mode.
package heartbeat

import (
	"math/rand/v2"
	"time"
)

const (
	DefaultInterval         = 2 * time.Minute
	DefaultFailureThreshold = 2
)

// NextDelay returns a duration in the inclusive range [75%, 125%] of base.
// Drawing a fresh value for every cycle prevents agents that start together
// from converging on the same reporting schedule again.
func NextDelay(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	delta := base / 4
	if delta == 0 {
		return base
	}
	span := int64(2*delta) + 1
	return base - delta + time.Duration(rand.Int64N(span))
}

// Observation describes the state transition produced by one health result.
type Observation struct {
	ConsecutiveFailures int
	Unavailable         bool
	BecameUnavailable   bool
	Recovered           bool
}

// FailureTracker applies the common consecutive-failure policy. It is owned by
// one reporting loop and therefore does not need internal synchronization.
type FailureTracker struct {
	threshold   int
	failures    int
	unavailable bool
}

func NewFailureTracker(threshold int) FailureTracker {
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	return FailureTracker{threshold: threshold}
}

// Observe records one result. A nil error clears the consecutive-failure
// count, and reports recovery only if the tracker had become unavailable.
func (t *FailureTracker) Observe(err error) Observation {
	if err == nil {
		recovered := t.unavailable
		t.failures = 0
		t.unavailable = false
		return Observation{Recovered: recovered}
	}

	t.failures++
	becameUnavailable := !t.unavailable && t.failures >= t.threshold
	if becameUnavailable {
		t.unavailable = true
	}
	return Observation{
		ConsecutiveFailures: t.failures,
		Unavailable:         t.unavailable,
		BecameUnavailable:   becameUnavailable,
	}
}
