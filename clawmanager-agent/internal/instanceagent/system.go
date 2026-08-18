package instanceagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func CollectRuntimeState(cfg Config, lastSkillScanAt time.Time) (StateReport, map[string]any) {
	sampledAt := time.Now().UTC()
	status, pid, version := inspectRuntime(cfg.RuntimeCommand)
	diskTotal, diskFree := diskStats(cfg.PersistentDir)
	diskUsed := diskTotal - diskFree
	persistentUsed := directorySize(cfg.PersistentDir)
	mem := memoryStats()
	cpuCores := cpuCores()
	cpuUsage := cpuUsagePercent()
	load1, load5, load15 := loadAverages()
	networkIfaces, networkRX, networkTX := networkStats()
	osInfo := osReleaseInfo()
	kernel := kernelRelease()
	hostname, _ := os.Hostname()
	diskUsage := percent(diskUsed, diskTotal)
	memoryUsage := percent(mem.used, mem.total)
	metricsCollector := "ok"
	var metricsErrors []string
	if cpuCores <= 0 {
		metricsCollector = "error"
		metricsErrors = append(metricsErrors, "cpu cores unavailable")
	}
	if mem.total <= 0 {
		metricsCollector = "error"
		metricsErrors = append(metricsErrors, "memory unavailable")
	}
	if diskTotal <= 0 {
		metricsCollector = "error"
		metricsErrors = append(metricsErrors, "disk unavailable")
	}
	bootstrapStatus, bootstrapDetail := bootstrapConfigHealth(cfg)

	report := StateReport{
		AgentID:    cfg.AgentID,
		ReportedAt: sampledAt,
		Runtime: RuntimeInfo{
			OpenClawStatus:  status,
			OpenClawPID:     pid,
			OpenClawVersion: version,
		},
		SystemInfo: SystemInfo{
			Runtime:            cfg.RuntimeType,
			OS:                 osInfo.id,
			OSName:             osInfo.prettyName,
			OSVersion:          osInfo.versionID,
			Kernel:             kernel,
			Arch:               runtime.GOARCH,
			Hostname:           hostname,
			DesktopBase:        "webtop",
			SampledAt:          sampledAt,
			CPUCores:           cpuCores,
			CPUUsagePercent:    cpuUsage,
			LoadAverage1:       load1,
			LoadAverage5:       load5,
			LoadAverage15:      load15,
			MemoryTotal:        mem.total,
			MemoryFree:         mem.free,
			MemoryAvailable:    mem.available,
			MemoryUsed:         mem.used,
			MemoryUsagePercent: memoryUsage,
			DiskTotalBytes:     diskTotal,
			DiskFreeBytes:      diskFree,
			DiskUsedBytes:      diskUsed,
			DiskUsagePercent:   diskUsage,
			DiskLimitBytes:     cfg.DiskLimitBytes,
			NetworkRxBytes:     networkRX,
			NetworkTxBytes:     networkTX,
			NetworkInterfaces:  networkIfaces,
			CPU: map[string]any{
				"cores":         cpuCores,
				"load":          map[string]float64{"1m": load1, "5m": load5, "15m": load15},
				"usage_percent": cpuUsage,
				"load_average":  []float64{load1, load5, load15},
			},
			Memory: map[string]any{
				"mem_total_bytes":     mem.total,
				"mem_available_bytes": mem.available,
				"total_bytes":         mem.total,
				"free_bytes":          mem.free,
				"available_bytes":     mem.available,
				"used_bytes":          mem.used,
				"usage_percent":       memoryUsage,
			},
			Disk: map[string]any{
				"mount_path":            cfg.PersistentDir,
				"root_total_bytes":      diskTotal,
				"root_free_bytes":       diskFree,
				"total_bytes":           diskTotal,
				"free_bytes":            diskFree,
				"used_bytes":            diskUsed,
				"usage_percent":         diskUsage,
				"limit_bytes":           cfg.DiskLimitBytes,
				"persistent_used_bytes": persistentUsed,
				"path":                  cfg.PersistentDir,
			},
			Network: map[string]any{
				"rx_bytes":   networkRX,
				"tx_bytes":   networkTX,
				"interfaces": networkIfaces,
			},
		},
		Health: HealthInfo{
			cfg.RuntimeType + "_process":      healthStatus(status),
			"desktop":                         "ok",
			"agent":                           "ok",
			"metrics_collector":               metricsCollector,
			"metrics_sample_interval_seconds": int(cfg.StateReportEvery.Seconds()),
			"last_skill_scan_at":              lastSkillScanAt,
			"runtime_command":                 cfg.RuntimeCommand,
			"persistent_dir":                  cfg.PersistentDir,
			"skill_dirs_configured":           cfg.SkillDirs,
			"bootstrap_config":                bootstrapStatus,
			"bootstrap_config_detail":         bootstrapDetail,
		},
	}
	if len(metricsErrors) > 0 {
		report.Health["metrics_error"] = strings.Join(metricsErrors, "; ")
	}

	summary := map[string]any{
		"runtime":                   cfg.RuntimeType,
		cfg.RuntimeType + "_status": status,
		cfg.RuntimeType + "_pid":    pid,
		"skill_count":               0,
		"active_skill_count":        0,
		"sampled_at":                sampledAt,
		"cpu_usage_percent":         cpuUsage,
		"cpu_cores":                 cpuCores,
		"memory_used_bytes":         mem.used,
		"memory_total_bytes":        mem.total,
		"memory_available_bytes":    mem.available,
		"disk_used_bytes":           diskUsed,
		"disk_limit_bytes":          cfg.DiskLimitBytes,
		"disk_free_bytes":           diskFree,
		"network_rx_bytes":          networkRX,
		"network_tx_bytes":          networkTX,
	}
	return report, summary
}

func inspectRuntime(command string) (string, int, string) {
	pid := findProcess(command)
	version := runtimeVersion(command)

	if pid > 0 {
		return "running", pid, version
	}
	if version != "" {
		return "running", 0, version
	}
	if _, err := exec.LookPath(command); err == nil {
		return "unknown", 0, ""
	}
	return "error", 0, ""
}

func bootstrapConfigHealth(cfg Config) (string, map[string]any) {
	paths := []string{
		filepath.Join(cfg.WorkDir(), "bootstrap", "applied-state.json"),
		filepath.Join("/config/.hermes/hermes-agent/bootstrap", "applied-state.json"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			return "error", map[string]any{"path": path, "error": err.Error()}
		}
		detail := map[string]any{"path": path}
		status := "ok"
		for _, section := range []string{"manifest", "channels"} {
			block, _ := state[section].(map[string]any)
			if block == nil {
				continue
			}
			if errText, _ := block["error"].(string); errText != "" {
				status = "error"
				detail[section+"_error"] = errText
			}
		}
		if channels, _ := state["channels"].(map[string]any); channels != nil {
			if platforms, ok := channels["configured_platforms"]; ok {
				detail["configured_platforms"] = platforms
			}
		}
		if skills, _ := state["skills"].(map[string]any); skills != nil {
			if sources, ok := skills["sources"]; ok {
				detail["skill_sources"] = sources
			}
		}
		return status, detail
	}
	return "ok", map[string]any{"applied_state": "not_found"}
}

func runtimeVersion(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if line == "" {
		return filepath.Base(command)
	}
	return line
}

func findProcess(command string) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	commandBase := filepath.Base(command)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		comm, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		text := string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '})) + " " + strings.TrimSpace(string(comm))
		if strings.Contains(text, "clawmanager-agent") {
			continue
		}
		if strings.Contains(text, commandBase) {
			return pid
		}
	}
	return 0
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

type memStats struct {
	total     int64
	free      int64
	available int64
	used      int64
}

type osInfo struct {
	id         string
	prettyName string
	versionID  string
}

func memoryStats() memStats {
	if stats, ok := cgroupMemoryStats(); ok {
		return stats
	}

	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return memStats{}
	}
	defer file.Close()

	values := map[string]int64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = kb * 1024
		}
	}
	stats := memStats{
		total:     values["MemTotal"],
		free:      values["MemFree"],
		available: values["MemAvailable"],
	}
	if stats.available == 0 {
		stats.available = stats.free
	}
	stats.used = stats.total - stats.available
	if stats.used < 0 {
		stats.used = 0
	}
	return stats
}

func cgroupMemoryStats() (memStats, bool) {
	limit, ok := readCgroupLimit("/sys/fs/cgroup/memory.max")
	current, currentOK := readIntFile("/sys/fs/cgroup/memory.current")
	if !ok {
		limit, ok = readCgroupLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes")
		current, currentOK = readIntFile("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	}
	if !ok || !currentOK || limit <= 0 || isUnboundedCgroupLimit(limit) {
		return memStats{}, false
	}
	available := limit - current
	if available < 0 {
		available = 0
	}
	return memStats{
		total:     limit,
		free:      available,
		available: available,
		used:      current,
	}, true
}

func osReleaseInfo() osInfo {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return osInfo{id: runtime.GOOS}
	}
	info := osInfo{id: runtime.GOOS}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch key {
		case "ID":
			info.id = value
		case "PRETTY_NAME":
			info.prettyName = value
		case "VERSION_ID":
			info.versionID = value
		}
	}
	return info
}

func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func loadAverages() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	return parseFloat(fields[0]), parseFloat(fields[1]), parseFloat(fields[2])
}

func cpuCores() float64 {
	if quota, period, ok := cgroupCPUQuota(); ok && quota > 0 && period > 0 {
		cores := float64(quota) / float64(period)
		if cores > 0 {
			return math.Round(cores*100) / 100
		}
	}
	if count := procCPUCount(); count > 0 {
		return float64(count)
	}
	return float64(runtime.NumCPU())
}

func cgroupCPUQuota() (quota int64, period int64, ok bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 && fields[0] != "max" {
			quota, quotaErr := strconv.ParseInt(fields[0], 10, 64)
			period, periodErr := strconv.ParseInt(fields[1], 10, 64)
			if quotaErr == nil && periodErr == nil {
				return quota, period, true
			}
		}
	}
	quota, quotaOK := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, periodOK := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quotaOK && periodOK && quota > 0 && period > 0 {
		return quota, period, true
	}
	return 0, 0, false
}

func procCPUCount() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "processor") {
			count++
		}
	}
	return count
}

type cpuSample struct {
	idle  uint64
	total uint64
}

func cpuUsagePercent() float64 {
	first, ok := readCPUSample()
	if !ok {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	second, ok := readCPUSample()
	if !ok || second.total <= first.total {
		return 0
	}
	totalDelta := second.total - first.total
	idleDelta := second.idle - first.idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0
	}
	return roundPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
}

func readCPUSample() (cpuSample, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return cpuSample{}, false
		}
		var values []uint64
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return cpuSample{}, false
			}
			values = append(values, value)
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		return cpuSample{idle: idle, total: total}, true
	}
	return cpuSample{}, false
}

func networkStats() ([]NetworkInterface, int64, int64) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, 0, 0
	}
	defer file.Close()

	var interfaces []NetworkInterface
	var totalRX int64
	var totalTX int64
	interfaceDetails := networkInterfaceDetails()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		namePart, statsPart, _ := strings.Cut(line, ":")
		name := strings.TrimSpace(namePart)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(statsPart)
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		detail := interfaceDetails[name]
		if detail.Status == "" {
			detail.Status = "unknown"
		}
		interfaces = append(interfaces, NetworkInterface{
			Name:      name,
			Status:    detail.Status,
			Addresses: detail.Addresses,
			RXBytes:   rx,
			TXBytes:   tx,
		})
		totalRX += rx
		totalTX += tx
	}
	return interfaces, totalRX, totalTX
}

type networkDetail struct {
	Status    string
	Addresses []string
}

func networkInterfaceDetails() map[string]networkDetail {
	result := map[string]networkDetail{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range interfaces {
		if iface.Name == "lo" {
			continue
		}
		status := "down"
		if iface.Flags&net.FlagUp != 0 {
			status = "up"
		}
		var addresses []string
		if addrs, err := iface.Addrs(); err == nil {
			for _, addr := range addrs {
				address := addr.String()
				if ip, _, err := net.ParseCIDR(address); err == nil {
					address = ip.String()
				}
				if address != "" {
					addresses = append(addresses, address)
				}
			}
		}
		result[iface.Name] = networkDetail{Status: status, Addresses: addresses}
	}
	return result
}

func readIntFile(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value := strings.TrimSpace(string(data))
	if value == "" || value == "max" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func readCgroupLimit(path string) (int64, bool) {
	value, ok := readIntFile(path)
	if !ok || value <= 0 || isUnboundedCgroupLimit(value) {
		return 0, false
	}
	return value, true
}

func isUnboundedCgroupLimit(value int64) bool {
	return value >= (1 << 60)
}

func parseFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func percent(used, total int64) float64 {
	if total <= 0 || used <= 0 {
		return 0
	}
	return roundPercent(100 * float64(used) / float64(total))
}

func roundPercent(value float64) float64 {
	return math.Round(value*100) / 100
}

func healthStatus(status string) string {
	switch status {
	case "running", "starting", "unknown":
		return "ok"
	default:
		return "error"
	}
}
