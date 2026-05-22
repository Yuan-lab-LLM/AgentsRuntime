package profiler

import (
	"testing"
	"time"
)

func TestCalculateCPUUsageForOneCore(t *testing.T) {
	start := time.Unix(100, 0)
	previous := cpuUsageSample{usageMicros: 1_000_000, sampledAt: start, ok: true}
	current := cpuUsageSample{usageMicros: 1_250_000, sampledAt: start.Add(time.Second), ok: true}

	percent, usedCores, ready := calculateCPUUsage(previous, current, 1)
	if !ready {
		t.Fatal("expected CPU usage sample to be ready")
	}
	if percent != 25 {
		t.Fatalf("usage percent = %v, want 25", percent)
	}
	if usedCores != 0.25 {
		t.Fatalf("used cores = %v, want 0.25", usedCores)
	}
}

func TestCalculateCPUUsageNormalizesByAvailableCores(t *testing.T) {
	start := time.Unix(100, 0)
	previous := cpuUsageSample{usageMicros: 1_000_000, sampledAt: start, ok: true}
	current := cpuUsageSample{usageMicros: 2_000_000, sampledAt: start.Add(time.Second), ok: true}

	percent, usedCores, ready := calculateCPUUsage(previous, current, 2)
	if !ready {
		t.Fatal("expected CPU usage sample to be ready")
	}
	if percent != 50 {
		t.Fatalf("usage percent = %v, want 50", percent)
	}
	if usedCores != 1 {
		t.Fatalf("used cores = %v, want 1", usedCores)
	}
}

func TestCalculateCPUUsageNeedsPreviousSample(t *testing.T) {
	start := time.Unix(100, 0)
	current := cpuUsageSample{usageMicros: 2_000_000, sampledAt: start.Add(time.Second), ok: true}

	percent, usedCores, ready := calculateCPUUsage(cpuUsageSample{}, current, 1)
	if ready {
		t.Fatal("expected first CPU usage sample to be not ready")
	}
	if percent != 0 || usedCores != 0 {
		t.Fatalf("first sample percent=%v usedCores=%v, want zeros", percent, usedCores)
	}
}

func TestCalculateCPUUsageClampsOverage(t *testing.T) {
	start := time.Unix(100, 0)
	previous := cpuUsageSample{usageMicros: 1_000_000, sampledAt: start, ok: true}
	current := cpuUsageSample{usageMicros: 4_000_000, sampledAt: start.Add(time.Second), ok: true}

	percent, usedCores, ready := calculateCPUUsage(previous, current, 1)
	if !ready {
		t.Fatal("expected CPU usage sample to be ready")
	}
	if percent != 100 {
		t.Fatalf("usage percent = %v, want 100", percent)
	}
	if usedCores != 3 {
		t.Fatalf("used cores = %v, want 3", usedCores)
	}
}
