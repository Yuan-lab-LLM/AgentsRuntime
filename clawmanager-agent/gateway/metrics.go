package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func BuildMetricsPayload(cfg Config, manager *GatewayManager, podID int) MetricsPayload {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	gatewayCount := manager.UsedSlots()
	return MetricsPayload{
		PodID:           podID,
		Namespace:       cfg.Namespace,
		PodName:         cfg.PodName,
		CPUMillisUsed:   0,
		MemoryBytesUsed: mem.Sys,
		DiskBytesUsed:   diskUsage(manager.GatewayStates()),
		NetworkRXBytes:  0,
		NetworkTXBytes:  0,
		Metrics: map[string]any{
			"gateway_count": gatewayCount,
			"resource_limits": map[string]any{
				"cpu_cgroup":       "degraded",
				"memory_cgroup":    "degraded",
				"filesystem_quota": "degraded",
			},
		},
		ReportedAt: time.Now().UTC(),
	}
}

func diskUsage(states []GatewayState) uint64 {
	var total uint64
	for _, state := range states {
		total += dirUsage(state.WorkspacePath)
	}
	return total
}

func dirUsage(root string) uint64 {
	var total uint64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 0 {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}
