package slots

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func metricsFixture() (*metricsSampler, map[string]string, *time.Time) {
	now := time.Unix(100, 0)
	files := map[string]string{
		"/proc/self/cgroup":                           "0::/pod/container\n",
		"/proc/self/mountinfo":                        "31 23 0:28 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/pod/container/memory.current": "1048576\n",
		"/sys/fs/cgroup/pod/container/cpu.stat":       "usage_usec 1000000\nuser_usec 750000\nsystem_usec 250000\n",
	}
	sampler := &metricsSampler{read: func(name string) ([]byte, error) {
		value, ok := files[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(value), nil
	}, now: func() time.Time { return now }}
	return sampler, files, &now
}

func TestCgroupMetricsParserAndCPUWindow(t *testing.T) {
	sam, files, now := metricsFixture()
	first := sam.Sample()
	if first.MemoryBytesUsed != 1048576 || first.MemoryState != "ok" || first.CPUState != "warming" {
		t.Fatalf("initial sample must expose warmup: %#v", first)
	}
	*now = now.Add(2 * time.Second)
	files["/sys/fs/cgroup/pod/container/cpu.stat"] = "usage_usec 4000000\n"
	second := sam.Sample()
	if second.CPUMillisUsed != 1500 || second.CPUState != "ok" {
		t.Fatalf("CPU must be 1.5 cores over the window: %#v", second)
	}
	*now = now.Add(time.Second)
	files["/sys/fs/cgroup/pod/container/cpu.stat"] = "usage_usec 1\n"
	if reset := sam.Sample(); reset.CPUState != "degraded" || len(reset.Degraded) == 0 {
		t.Fatalf("counter reset was reported as valid zero CPU: %#v", reset)
	}
	*now = now.Add(time.Second)
	files["/sys/fs/cgroup/pod/container/cpu.stat"] = "usage_usec 1000001\n"
	if recovered := sam.Sample(); recovered.CPUMillisUsed != 1000 || recovered.CPUState != "ok" {
		t.Fatalf("counter reset did not establish a new baseline: %#v", recovered)
	}
}

func TestCgroupMetricsHonorsVisibleMountRootAndFailsExplicitly(t *testing.T) {
	sam, files, _ := metricsFixture()
	files["/proc/self/mountinfo"] = "31 23 0:28 /pod/container /visible/cgroup ro - cgroup2 cgroup rw\n"
	files["/visible/cgroup/memory.current"] = "4096"
	files["/visible/cgroup/cpu.stat"] = "usage_usec 15"
	if sample := sam.Sample(); sample.MemoryBytesUsed != 4096 {
		t.Fatalf("wrong cgroup scope: %#v", sample)
	}
	for name, change := range map[string]func(map[string]string){
		"v1": func(files map[string]string) { files["/proc/self/cgroup"] = "2:cpu,cpuacct:/container\n" },
		"outside mount root": func(files map[string]string) {
			files["/proc/self/mountinfo"] = "31 23 0:28 /other /sys/fs/cgroup ro - cgroup2 cgroup rw\n"
		},
		"invalid memory": func(files map[string]string) { files["/sys/fs/cgroup/pod/container/memory.current"] = "not-a-counter" },
		"invalid CPU":    func(files map[string]string) { files["/sys/fs/cgroup/pod/container/cpu.stat"] = "usage_usec -1" },
	} {
		t.Run(name, func(t *testing.T) {
			sam, files, _ := metricsFixture()
			change(files)
			sample := sam.Sample()
			if len(sample.Degraded) == 0 {
				t.Fatalf("missing explicit degraded status: %#v", sample)
			}
		})
	}
}

func TestPoolMetricsAggregatesContainersAndCountsNetworkOnce(t *testing.T) {
	coordinator := containerMetrics{CPUMillisUsed: 100, MemoryBytesUsed: 1000, CPUState: "ok", MemoryState: "ok"}
	slots := []containerMetrics{{CPUMillisUsed: 200, MemoryBytesUsed: 2000, CPUState: "ok", MemoryState: "ok"}, {CPUMillisUsed: 300, MemoryBytesUsed: 3000, CPUState: "ok", MemoryState: "ok"}}
	network, err := parsePodNetwork([]byte("Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n eth0: 11 0 0 0 0 0 0 0 22 0 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	payload := aggregateMetrics(gateway.Config{Namespace: "runtime", PodName: "pool"}, 7, coordinator, slots, network)
	if payload.CPUMillisUsed != 600 || payload.MemoryBytesUsed != 6000 || payload.NetworkRXBytes != 11 || payload.NetworkTXBytes != 22 {
		t.Fatalf("bad aggregation or duplicated network: %#v", payload)
	}
	if payload.DiskBytesUsed != 0 || payload.Metrics["disk_collection"] != "degraded_not_collected" {
		t.Fatal("unmeasured disk was presented as measured")
	}
	slots[1] = unavailableContainerMetrics("slot_unavailable")
	payload = aggregateMetrics(gateway.Config{}, 7, coordinator, slots, network)
	if payload.CPUMillisUsed != 300 || payload.MemoryBytesUsed != 3000 || payload.Metrics["containers_complete"] != false {
		t.Fatalf("missing slot silently became healthy zero: %#v", payload)
	}
}

func TestPoolMetricsCollectsIndependentWorkersWithoutSockets(t *testing.T) {
	p := &Pool{count: 2, call: func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if method != http.MethodGet || path != "/v1/slot-metrics" {
			return 0, nil, fmt.Errorf("unexpected metrics call")
		}
		if index == 1 {
			return 0, nil, fmt.Errorf("unavailable worker")
		}
		data, _ := json.Marshal(containerMetrics{CPUMillisUsed: 250, MemoryBytesUsed: 2048, CPUState: "ok", MemoryState: "ok"})
		return 200, data, nil
	}}
	slots := p.collectMetrics(context.Background())
	if len(slots) != 2 || slots[0].MemoryBytesUsed != 2048 || len(slots[1].Degraded) == 0 {
		t.Fatalf("worker failure hidden: %#v", slots)
	}
}

func TestWorkerMetricsEndpointAuthenticatesAndUsesCgroup(t *testing.T) {
	sam, _, _ := metricsFixture()
	w := &Worker{cfg: gateway.Config{ControlToken: "control"}, metrics: sam}
	for _, authorized := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/v1/slot-metrics", nil)
		if authorized {
			req.Header.Set(gateway.ControlTokenHeader, "control")
		}
		out := httptest.NewRecorder()
		w.ServeHTTP(out, req)
		if !authorized {
			if out.Code != 401 {
				t.Fatal("metrics endpoint lacked auth")
			}
			continue
		}
		if out.Code != 200 || !strings.Contains(out.Body.String(), `"memory_bytes_used":1048576`) {
			t.Fatalf("endpoint did not use cgroup accounting: %d %s", out.Code, out.Body.String())
		}
	}
}
