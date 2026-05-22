package profiler

import (
	"bufio"
	"bytes"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

type Profiler struct {
	diskUsagePath  string
	diskLimitBytes uint64
	runtimeType    string
	runtimeName    string
	desktopBase    string
	cpuMu          sync.Mutex
	lastCPU        cpuUsageSample
}

type cpuUsageSample struct {
	usageMicros uint64
	sampledAt   time.Time
	source      string
	ok          bool
}

func New(cfg appconfig.Config) *Profiler {
	return &Profiler{
		diskUsagePath:  cfg.DiskUsagePath,
		diskLimitBytes: cfg.DiskLimitBytes,
		runtimeType:    cfg.RuntimeType,
		runtimeName:    cfg.RuntimeName,
		desktopBase:    cfg.DesktopBase,
	}
}

func (p *Profiler) Collect() map[string]any {
	hostname, _ := os.Hostname()
	kernel := readTrimmed("/proc/sys/kernel/osrelease")
	osVersion := detectOSVersion()
	hostMemTotalKB, hostMemAvailableKB := readHostMemInfo()
	load1, load5, load15 := readLoadAvg()
	cpuInfo := p.collectCPUInfo(load1, load5, load15)
	memoryInfo := collectMemoryInfo(hostMemTotalKB, hostMemAvailableKB)
	diskInfo := p.collectDiskInfo()

	return map[string]any{
		"runtime_type": p.runtimeType,
		"runtime_name": p.runtimeName,
		"desktop_base": p.desktopBase,
		"hostname":     hostname,
		"os": map[string]any{
			"goos":       runtime.GOOS,
			"goarch":     runtime.GOARCH,
			"kernel":     kernel,
			"os_release": osVersion,
			"go_version": runtime.Version(),
			"build_info": readBuildInfo(),
		},
		"cpu":     cpuInfo,
		"memory":  memoryInfo,
		"disk":    diskInfo,
		"network": collectNetworkTraffic(),
		"host": map[string]any{
			"cpu": map[string]any{
				"load": map[string]any{
					"1m":  load1,
					"5m":  load5,
					"15m": load15,
				},
				"cores": runtime.NumCPU(),
			},
			"memory": map[string]any{
				"mem_total_kb":        hostMemTotalKB,
				"mem_available_kb":    hostMemAvailableKB,
				"mem_total_bytes":     hostMemTotalKB * 1024,
				"mem_available_bytes": hostMemAvailableKB * 1024,
			},
		},
	}
}

func detectOSVersion() string {
	if value := readTrimmed("/etc/os-release"); value != "" {
		scanner := bufio.NewScanner(strings.NewReader(value))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	return ""
}

func readHostMemInfo() (uint64, uint64) {
	raw := readTrimmed("/proc/meminfo")
	var total uint64
	var available uint64

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value
		case "MemAvailable:":
			available = value
		}
	}
	return total, available
}

func (p *Profiler) collectCPUInfo(load1 float64, load5 float64, load15 float64) map[string]any {
	cores, availableCores, source := containerCPUCapacity()
	usagePercent, usedCores, usageReady, usageSource := p.collectCPUUsage(availableCores)

	result := map[string]any{
		"cores":             cores,
		"available_cores":   roundFloat(availableCores, 3),
		"scope":             "container",
		"data_source":       source,
		"usage_percent":     usagePercent,
		"used_cores":        usedCores,
		"usage_ready":       usageReady,
		"usage_data_source": usageSource,
		"load": map[string]any{
			"1m":  load1,
			"5m":  load5,
			"15m": load15,
		},
		"load_scope":       "host",
		"load_data_source": "/proc/loadavg",
	}
	if source == "cgroup" {
		result["quota_cores"] = roundFloat(availableCores, 3)
	}
	return result
}

func (p *Profiler) collectCPUUsage(availableCores float64) (float64, float64, bool, string) {
	current, ok := readContainerCPUUsage()
	if !ok || availableCores <= 0 {
		return 0, 0, false, current.source
	}

	p.cpuMu.Lock()
	defer p.cpuMu.Unlock()

	previous := p.lastCPU
	p.lastCPU = current
	usagePercent, usedCores, ready := calculateCPUUsage(previous, current, availableCores)
	return usagePercent, usedCores, ready, current.source
}

func collectMemoryInfo(hostTotalKB uint64, hostAvailableKB uint64) map[string]any {
	totalBytes, availableBytes, source := readContainerMemory()
	if totalBytes == 0 {
		totalBytes = hostTotalKB * 1024
		availableBytes = hostAvailableKB * 1024
		source = "host"
	}

	return map[string]any{
		"goroutines":          runtime.NumGoroutine(),
		"mem_total_kb":        totalBytes / 1024,
		"mem_available_kb":    availableBytes / 1024,
		"mem_total_bytes":     totalBytes,
		"mem_available_bytes": availableBytes,
		"scope":               "container",
		"data_source":         source,
	}
}

func (p *Profiler) collectDiskInfo() map[string]any {
	path := p.diskUsagePath
	if path == "" {
		path = "/config"
	}

	usedBytes, err := directorySize(path)
	if err != nil {
		return map[string]any{
			"path":        path,
			"scope":       "container_allocation",
			"data_source": "walk",
			"error":       err.Error(),
		}
	}

	freeBytes := uint64(0)
	if p.diskLimitBytes > usedBytes {
		freeBytes = p.diskLimitBytes - usedBytes
	}

	result := map[string]any{
		"path":            path,
		"used_bytes":      usedBytes,
		"root_used_bytes": usedBytes,
		"scope":           "container_allocation",
		"data_source":     "walk",
	}
	if p.diskLimitBytes > 0 {
		result["limit_bytes"] = p.diskLimitBytes
		result["free_bytes"] = freeBytes
		result["root_total_bytes"] = p.diskLimitBytes
		result["root_free_bytes"] = freeBytes
	}
	return result
}

func readLoadAvg() (float64, float64, float64) {
	fields := strings.Fields(readTrimmed("/proc/loadavg"))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(value string) float64 {
		v, _ := strconv.ParseFloat(value, 64)
		return v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

func collectNetworkTraffic() map[string]any {
	raw := readTrimmed("/proc/net/dev")
	if raw == "" {
		return map[string]any{
			"rx_bytes":    uint64(0),
			"tx_bytes":    uint64(0),
			"interfaces":  []map[string]any{},
			"scope":       "pod_network_namespace",
			"data_source": "/proc/net/dev",
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(raw))
	result := make([]map[string]any, 0)
	var totalRX uint64
	var totalTX uint64
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}

		rxBytes, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		txBytes, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}

		totalRX += rxBytes
		totalTX += txBytes
		result = append(result, map[string]any{
			"name":     name,
			"rx_bytes": rxBytes,
			"tx_bytes": txBytes,
		})
	}

	return map[string]any{
		"rx_bytes":    totalRX,
		"tx_bytes":    totalTX,
		"interfaces":  result,
		"scope":       "pod_network_namespace",
		"data_source": "/proc/net/dev",
	}
}

func readContainerCPULimit() (int, bool) {
	if _, cores, ok := readContainerCPUQuotaCores(); ok {
		return quotaToCores(cores), true
	}
	if cpus, ok := readCpusetLimit(); ok && cpus > 0 {
		return cpus, true
	}
	return 0, false
}

func readContainerMemory() (uint64, uint64, string) {
	if current, max, ok := readCgroupV2Memory(); ok {
		available := uint64(0)
		if max > current {
			available = max - current
		}
		return max, available, "cgroup"
	}
	if current, max, ok := readCgroupV1Memory(); ok {
		available := uint64(0)
		if max > current {
			available = max - current
		}
		return max, available, "cgroup"
	}
	return 0, 0, ""
}

func containerCPUCapacity() (int, float64, string) {
	hostCores := runtime.NumCPU()
	cores := hostCores
	availableCores := float64(hostCores)
	source := "host"

	if _, quotaCores, ok := readContainerCPUQuotaCores(); ok {
		availableCores = quotaCores
		cores = quotaToCores(quotaCores)
		source = "cgroup"
	} else if cpus, ok := readCpusetLimit(); ok && cpus > 0 {
		cores = cpus
		availableCores = float64(cpus)
		source = "cpuset"
	}
	if availableCores <= 0 {
		availableCores = 1
	}
	if cores < 1 {
		cores = 1
	}
	return cores, availableCores, source
}

func readContainerCPUQuotaCores() (string, float64, bool) {
	if quota, period, ok := readCgroupV2CPUQuota(); ok {
		return "cgroup_v2:/sys/fs/cgroup/cpu.max", quota / period, true
	}
	if quota, period, ok := readCgroupV1CPUQuota(); ok {
		return "cgroup_v1:cpu.cfs_quota_us", quota / period, true
	}
	return "", 0, false
}

func readContainerCPUUsage() (cpuUsageSample, bool) {
	if usageMicros, ok := readCgroupV2CPUUsage(); ok {
		return cpuUsageSample{
			usageMicros: usageMicros,
			sampledAt:   time.Now(),
			source:      "cgroup_v2:/sys/fs/cgroup/cpu.stat",
			ok:          true,
		}, true
	}
	if usageMicros, source, ok := readCgroupV1CPUUsage(); ok {
		return cpuUsageSample{
			usageMicros: usageMicros,
			sampledAt:   time.Now(),
			source:      source,
			ok:          true,
		}, true
	}
	return cpuUsageSample{}, false
}

func readCgroupV2CPUUsage() (uint64, bool) {
	raw := readTrimmed("/sys/fs/cgroup/cpu.stat")
	if raw == "" {
		return 0, false
	}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "usage_usec" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

func readCgroupV1CPUUsage() (uint64, string, bool) {
	for _, path := range []string{
		"/sys/fs/cgroup/cpuacct/cpuacct.usage",
		"/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage",
		"/sys/fs/cgroup/cpu/cpuacct.usage",
	} {
		usageNanos, ok := readUintFromFile(path)
		if !ok {
			continue
		}
		return usageNanos / 1000, "cgroup_v1:" + path, true
	}
	return 0, "", false
}

func calculateCPUUsage(previous cpuUsageSample, current cpuUsageSample, availableCores float64) (float64, float64, bool) {
	if !previous.ok || !current.ok || current.usageMicros < previous.usageMicros || !current.sampledAt.After(previous.sampledAt) || availableCores <= 0 {
		return 0, 0, false
	}

	elapsedMicros := float64(current.sampledAt.Sub(previous.sampledAt).Microseconds())
	if elapsedMicros <= 0 {
		return 0, 0, false
	}

	usedCores := float64(current.usageMicros-previous.usageMicros) / elapsedMicros
	usagePercent := usedCores / availableCores * 100
	if usagePercent < 0 {
		usagePercent = 0
	}
	if usagePercent > 100 {
		usagePercent = 100
	}
	return roundFloat(usagePercent, 2), roundFloat(usedCores, 3), true
}

func readCgroupV2CPUQuota() (float64, float64, bool) {
	raw := readTrimmed("/sys/fs/cgroup/cpu.max")
	if raw == "" {
		return 0, 0, false
	}

	fields := strings.Fields(raw)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, 0, false
	}

	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, false
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || period <= 0 {
		return 0, 0, false
	}
	return quota, period, true
}

func readCgroupV1CPUQuota() (float64, float64, bool) {
	quota := readTrimmed("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period := readTrimmed("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quota == "" || period == "" || quota == "-1" {
		return 0, 0, false
	}

	quotaValue, err := strconv.ParseFloat(quota, 64)
	if err != nil {
		return 0, 0, false
	}
	periodValue, err := strconv.ParseFloat(period, 64)
	if err != nil || periodValue <= 0 {
		return 0, 0, false
	}
	return quotaValue, periodValue, true
}

func quotaToCores(coresFloat float64) int {
	cores := int(math.Ceil(coresFloat))
	if cores < 1 {
		cores = 1
	}
	return cores
}

func readCpusetLimit() (int, bool) {
	for _, path := range []string{
		"/sys/fs/cgroup/cpuset.cpus.effective",
		"/sys/fs/cgroup/cpuset/cpuset.cpus",
	} {
		raw := readTrimmed(path)
		if raw == "" {
			continue
		}
		if count := parseCPUSet(raw); count > 0 {
			return count, true
		}
	}
	return 0, false
}

func parseCPUSet(raw string) int {
	total := 0
	for _, part := range strings.Split(strings.TrimSpace(raw), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "-") {
			total++
			continue
		}
		bounds := strings.SplitN(part, "-", 2)
		if len(bounds) != 2 {
			continue
		}
		start, err := strconv.Atoi(bounds[0])
		if err != nil {
			continue
		}
		end, err := strconv.Atoi(bounds[1])
		if err != nil || end < start {
			continue
		}
		total += end - start + 1
	}
	return total
}

func readCgroupV2Memory() (uint64, uint64, bool) {
	current, ok := readUintFromFile("/sys/fs/cgroup/memory.current")
	if !ok {
		return 0, 0, false
	}
	max, ok := readCgroupMemoryMax("/sys/fs/cgroup/memory.max")
	if !ok {
		return 0, 0, false
	}
	return current, max, true
}

func readCgroupV1Memory() (uint64, uint64, bool) {
	current, ok := readUintFromFile("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	if !ok {
		return 0, 0, false
	}
	max, ok := readCgroupMemoryMax("/sys/fs/cgroup/memory/memory.limit_in_bytes")
	if !ok {
		return 0, 0, false
	}
	return current, max, true
}

func readCgroupMemoryMax(path string) (uint64, bool) {
	raw := readTrimmed(path)
	if raw == "" || raw == "max" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || value > (1<<62) {
		return 0, false
	}
	return value, true
}

func readUintFromFile(path string) (uint64, bool) {
	raw := readTrimmed(path)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func roundFloat(value float64, places int) float64 {
	if places <= 0 {
		return math.Round(value)
	}
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}

func directorySize(root string) (uint64, error) {
	info, err := os.Stat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return uint64(info.Size()), nil
	}

	var total uint64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func readBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	var buf bytes.Buffer
	buf.WriteString(info.GoVersion)
	if info.Main.Path != "" {
		buf.WriteString(" ")
		buf.WriteString(info.Main.Path)
	}
	if info.Main.Version != "" {
		buf.WriteString("@")
		buf.WriteString(info.Main.Version)
	}
	return strings.TrimSpace(buf.String())
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
