package slots

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// Container accounting includes the supervisor, gateway and every descendant
// in its container cgroup. It is neither Go heap accounting nor a PID estimate.
type containerMetrics struct {
	Source          string    `json:"source"`
	CPUMillisUsed   int64     `json:"cpu_millis_used"`
	MemoryBytesUsed uint64    `json:"memory_bytes_used"`
	CPUState        string    `json:"cpu_state"`
	MemoryState     string    `json:"memory_state"`
	Degraded        []string  `json:"degraded,omitempty"`
	ReportedAt      time.Time `json:"reported_at"`
}

type metricsSampler struct {
	mu            sync.Mutex
	read          func(string) ([]byte, error)
	now           func() time.Time
	directory     string
	previousUsage uint64
	previousTime  time.Time
	haveCPU       bool
}

func newMetricsSampler() *metricsSampler { return &metricsSampler{read: os.ReadFile, now: time.Now} }

func unavailableContainerMetrics(reason string) containerMetrics {
	return containerMetrics{Source: "cgroup_v2", CPUState: "degraded", MemoryState: "degraded", Degraded: []string{reason}, ReportedAt: time.Now().UTC()}
}

func (s *metricsSampler) Sample() containerMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	result := containerMetrics{Source: "cgroup_v2", CPUState: "degraded", MemoryState: "degraded", ReportedAt: now.UTC()}
	if s.directory == "" {
		directory, err := locateCgroupV2(s.read)
		if err != nil {
			result.Degraded = []string{"cgroup_v2_unavailable"}
			return result
		}
		s.directory = directory
	}
	if raw, err := s.read(filepath.Join(s.directory, "memory.current")); err == nil {
		if value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			result.MemoryBytesUsed = value
			result.MemoryState = "ok"
		}
	}
	if result.MemoryState != "ok" {
		result.Degraded = append(result.Degraded, "memory_counter_unavailable")
	}
	raw, err := s.read(filepath.Join(s.directory, "cpu.stat"))
	usage, parseErr := parseCPUUsage(raw)
	if err != nil || parseErr != nil {
		s.haveCPU = false
		result.Degraded = append(result.Degraded, "cpu_counter_unavailable")
		return result
	}
	switch {
	case !s.haveCPU:
		result.CPUState = "warming"
		result.Degraded = append(result.Degraded, "cpu_warming_up")
	case usage < s.previousUsage:
		result.Degraded = append(result.Degraded, "cpu_counter_reset")
	case !now.After(s.previousTime):
		result.Degraded = append(result.Degraded, "cpu_invalid_window")
	default:
		// One CPU-second per wall second is 1000 millicores, including children.
		millis := float64(usage-s.previousUsage) / now.Sub(s.previousTime).Seconds() / 1000
		if millis >= float64(math.MaxInt64) {
			result.Degraded = append(result.Degraded, "cpu_counter_overflow")
		} else {
			result.CPUMillisUsed = int64(millis)
			result.CPUState = "ok"
		}
	}
	s.previousUsage, s.previousTime, s.haveCPU = usage, now, true
	return result
}

func parseCPUUsage(raw []byte) (uint64, error) {
	var usage uint64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "usage_usec" {
			continue
		}
		if found || len(fields) != 2 {
			return 0, fmt.Errorf("invalid cgroup CPU counter")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid cgroup CPU counter")
		}
		usage, found = value, true
	}
	if !found {
		return 0, fmt.Errorf("cgroup CPU counter missing")
	}
	return usage, nil
}

func locateCgroupV2(read func(string) ([]byte, error)) (string, error) {
	self, err := read("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var current string
	for _, line := range strings.Split(string(self), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && fields[0] == "0" && fields[1] == "" {
			current = fields[2]
			break
		}
	}
	if current == "" || !path.IsAbs(current) || path.Clean(current) != current {
		return "", fmt.Errorf("cgroup v2 unavailable")
	}
	mounts, err := read("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var directory string
	longest := -1
	for _, line := range strings.Split(string(mounts), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields, filesystem := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(fields) < 5 || len(filesystem) == 0 || filesystem[0] != "cgroup2" {
			continue
		}
		root, mountpoint := unescape.Replace(fields[3]), unescape.Replace(fields[4])
		if !path.IsAbs(root) || !path.IsAbs(mountpoint) || path.Clean(root) != root || path.Clean(mountpoint) != mountpoint {
			continue
		}
		relative, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(current))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > longest {
			directory = filepath.Join(filepath.FromSlash(mountpoint), relative)
			longest = len(root)
		}
	}
	if directory == "" {
		return "", fmt.Errorf("current cgroup has no visible v2 mount")
	}
	return directory, nil
}

type podNetwork struct {
	RX, TX uint64
	State  string
}

func parsePodNetwork(raw []byte) (podNetwork, error) {
	result := podNetwork{State: "ok"}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		separator := strings.LastIndex(line, ":")
		if separator < 0 {
			continue
		}
		name, fields := strings.TrimSpace(line[:separator]), strings.Fields(line[separator+1:])
		if name == "" || len(fields) != 16 {
			return podNetwork{}, fmt.Errorf("invalid network counters")
		}
		rx, errRX := strconv.ParseUint(fields[0], 10, 64)
		tx, errTX := strconv.ParseUint(fields[8], 10, 64)
		if errRX != nil || errTX != nil {
			return podNetwork{}, fmt.Errorf("invalid network counters")
		}
		found = true
		if name == "lo" {
			continue
		}
		if math.MaxUint64-result.RX < rx || math.MaxUint64-result.TX < tx {
			return podNetwork{}, fmt.Errorf("network counters overflow")
		}
		result.RX += rx
		result.TX += tx
	}
	if !found {
		return podNetwork{}, fmt.Errorf("network counters missing")
	}
	return result, nil
}

func readPodNetwork() podNetwork {
	raw, err := os.ReadFile("/proc/net/dev")
	if err == nil {
		if counters, err := parsePodNetwork(raw); err == nil {
			return counters
		}
	}
	return podNetwork{State: "degraded"}
}

func (p *Pool) collectMetrics(ctx context.Context) []containerMetrics {
	result := make([]containerMetrics, p.count)
	var workers sync.WaitGroup
	for index := range result {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			status, raw, err := p.call(ctx, index, http.MethodGet, "/v1/slot-metrics", nil)
			var sample containerMetrics
			if err != nil || status != http.StatusOK || json.Unmarshal(raw, &sample) != nil || sample.CPUMillisUsed < 0 ||
				(sample.CPUState != "ok" && sample.CPUState != "warming" && sample.CPUState != "degraded") ||
				(sample.MemoryState != "ok" && sample.MemoryState != "degraded") {
				result[index] = unavailableContainerMetrics("slot_unavailable")
				return
			}
			result[index] = sample
		}(index)
	}
	workers.Wait()
	return result
}

func aggregateMetrics(cfg gateway.Config, podID int, coordinator containerMetrics, slots []containerMetrics, network podNetwork) gateway.MetricsPayload {
	containers := append([]containerMetrics{coordinator}, slots...)
	result := gateway.MetricsPayload{PodID: podID, Namespace: cfg.Namespace, PodName: cfg.PodName, NetworkRXBytes: network.RX, NetworkTXBytes: network.TX, ReportedAt: time.Now().UTC()}
	cpuState, memoryState := "ok", "ok"
	complete := true
	for _, sample := range containers {
		if sample.CPUState == "ok" && sample.CPUMillisUsed >= 0 && math.MaxInt64-result.CPUMillisUsed >= sample.CPUMillisUsed {
			result.CPUMillisUsed += sample.CPUMillisUsed
		} else {
			cpuState = "degraded"
			complete = false
		}
		if sample.MemoryState == "ok" && math.MaxUint64-result.MemoryBytesUsed >= sample.MemoryBytesUsed {
			result.MemoryBytesUsed += sample.MemoryBytesUsed
		} else {
			memoryState = "degraded"
			complete = false
		}
	}
	result.Metrics = map[string]any{
		"accounting_source": "container_cgroup_v2", "containers": containers, "containers_complete": complete,
		"slot_count": len(slots), "cpu_unit": "millicores", "network_collection": network.State,
		"network_scope": "pod_non_loopback_interfaces_once", "disk_collection": "degraded_not_collected",
		"resource_limits": map[string]any{"cpu_cgroup": cpuState, "memory_cgroup": memoryState, "filesystem_quota": "degraded"},
	}
	return result
}
