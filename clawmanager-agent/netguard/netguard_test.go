package netguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type invocation struct {
	tool, input string
	args        []string
}
type recorder struct {
	calls  []invocation
	fail   func(invocation) error
	output func(invocation) []byte
	kernel *kernelState
}

func (r *recorder) run(_ context.Context, input, tool string, args ...string) ([]byte, error) {
	c := invocation{tool, input, append([]string(nil), args...)}
	r.calls = append(r.calls, c)
	if r.fail != nil {
		if err := r.fail(c); err != nil {
			return nil, err
		}
	}
	var data []byte
	if r.kernel != nil {
		var err error
		data, err = r.kernel.run(context.Background(), input, tool, args...)
		if err != nil {
			return data, err
		}
	}
	if r.output != nil {
		return r.output(c), nil
	}
	return data, nil
}
func testConfig(t *testing.T) Config {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return Config{SlotIndex: 0, SlotCount: 1, StateDir: dir, PodUID: "pod-123"}
}
func testHandle(t *testing.T, r *recorder) *Handle {
	t.Helper()
	h, err := newHandle(testConfig(t), Identity{1, 1}, r.run)
	if err != nil {
		t.Fatal(err)
	}
	r.kernel = &kernelState{namespaceFile: filepath.Join(filepath.Dir(h.statePath), "fake-namespace")}
	h.statNamespace = func() (os.FileInfo, error) { return os.Lstat(r.kernel.namespaceFile) }
	return h
}

func TestConfigRejectsUnsafeAndOutOfBudgetValues(t *testing.T) {
	base := testConfig(t)
	for _, change := range []func(*Config){
		func(c *Config) { c.SlotCount = 0 }, func(c *Config) { c.SlotCount = 257 },
		func(c *Config) { c.SlotIndex = -1 }, func(c *Config) { c.SlotIndex = c.SlotCount },
		func(c *Config) { c.PodUID = "" }, func(c *Config) { c.PodUID = "../../workspace" },
		func(c *Config) { c.PodUID = "pod\nname" }, func(c *Config) { c.StateDir = "relative" },
		func(c *Config) { c.StateDir += "/../other" }, func(c *Config) { c.StateDir = "/" },
	} {
		cfg := base
		change(&cfg)
		if _, err := newHandle(cfg, Identity{1, 1}, (&recorder{}).run); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
	for _, id := range []Identity{{0, 1}, {1, 0}, {-1, 1}} {
		if _, err := newHandle(base, id, (&recorder{}).run); err == nil {
			t.Errorf("accepted identity %+v", id)
		}
	}
}

func TestSlotAddressBudgetAndUniqueResourceNames(t *testing.T) {
	seen := map[string]bool{}
	for _, count := range []int{1, 4, 8, 16, 256} {
		cfg := testConfig(t)
		cfg.SlotCount = count
		cfg.SlotIndex = count - 1
		h, err := newHandle(cfg, Identity{1, 1}, (&recorder{}).run)
		if err != nil {
			t.Fatal(err)
		}
		n := 4 * (count - 1)
		if h.ParentIP() != fmt.Sprintf("169.254.%d.%d", 64+n/256, n%256+1) || h.ChildIP() != fmt.Sprintf("169.254.%d.%d", 64+n/256, n%256+2) {
			t.Fatalf("bad /30 budget for %d: %s %s", count, h.ParentIP(), h.ChildIP())
		}
		for _, name := range []string{h.name, h.host, h.peer, h.table} {
			if len(name) > 15 || seen[name] {
				t.Fatalf("invalid/colliding resource %q", name)
			}
			seen[name] = true
		}
		if h.NamespacePath() != filepath.Join("/run/netns", h.name) {
			t.Fatal(h.NamespacePath())
		}
	}
}

func TestPrivateDirectoryRejectsSymlinksAndOpenPermissions(t *testing.T) {
	cfg := testConfig(t)
	if err := privateDirectory(cfg.StateDir, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(cfg.StateDir, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted public state directory")
	}
	if err := os.Chmod(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cfg.StateDir, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(link, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := privateDirectory(cfg.StateDir, uint32(os.Geteuid()+1)); err == nil {
		t.Fatal("accepted wrong owner")
	}
	parent := filepath.Join(cfg.StateDir, "writable")
	child := filepath.Join(parent, "network")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(child, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted writable state ancestor")
	}
}

func TestCleanupToleratesOnlyProvenAbsentResources(t *testing.T) {
	r := &recorder{}
	h := testHandle(t, r)
	if err := h.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.kernel.namespaceFile); err != nil {
		t.Fatal(err)
	}
	r.fail = func(c invocation) error {
		if len(c.args) > 0 && (c.args[0] == "delete" || c.args[0] == "link" && c.args[1] == "delete" || c.args[0] == "netns" && c.args[1] == "delete") {
			return errors.New("resource already gone")
		}
		return nil
	}
	r.output = func(c invocation) []byte {
		if c.tool == nftTool {
			return []byte(`{"nftables":[]}`)
		}
		return []byte(`[]`)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("proven absence not accepted: %v", err)
	}
	if h.linkOwned || h.tableOwned || h.namespaceOwned {
		t.Fatal("absent resource ownership retained")
	}
}

func TestPrepareInstallsExactPoliciesBeforeReady(t *testing.T) {
	r := &recorder{}
	h := testHandle(t, r)
	if err := h.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	if !h.ready {
		t.Fatal("successful preparation not ready")
	}
	data, err := os.ReadFile(h.ResolvConf())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "nameserver "+h.ParentIP()+"\noptions timeout:2 attempts:2\n" {
		t.Fatal(string(data))
	}
	info, err := os.Stat(h.ResolvConf())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("resolver privacy: %v %v", info, err)
	}
	var parent, child string
	for _, c := range r.calls {
		joined := strings.Join(c.args, " ")
		if strings.Contains(joined, "netns exec") || strings.Contains(joined, "ip_forward") || strings.Contains(c.input, "flush ruleset") {
			t.Fatalf("unsafe command: %+v", c)
		}
		if c.tool == nftTool {
			parent = c.input
		}
		if c.tool == nsenterTool && strings.Contains(joined, nftTool) {
			child = c.input
		}
		if !filepath.IsAbs(c.tool) {
			t.Fatal("nonabsolute tool", c.tool)
		}
	}
	for _, want := range []string{"iifname \"" + h.host + "\" ip saddr " + h.ChildIP() + " ip daddr " + h.ParentIP() + " tcp dport { 53, 31280, 31281, 31282, 31283 } accept", "iifname \"" + h.host + "\" ip saddr " + h.ChildIP() + " ip daddr " + h.ParentIP() + " udp dport 53 accept", "chain forward", "iifname \"" + h.host + "\" reject"} {
		if !strings.Contains(parent, want) {
			t.Errorf("missing parent restriction %q:\n%s", want, parent)
		}
	}
	for _, want := range []string{"ct direction reply return", "meta skuid >= 200000 meta l4proto tcp redirect to :15000", "ip daddr 127.0.0.0/8 return", "ip6 daddr ::1 return", "ip daddr " + h.ParentIP() + " tcp dport { 53, 31280, 31281, 31282, 31283 } return", "policy drop", "oifname \"lo\" accept", "ip daddr 127.0.0.0/8 accept", "ip6 daddr ::1 accept", "ip daddr " + h.ParentIP() + " udp dport 53 accept"} {
		if !strings.Contains(child, want) {
			t.Errorf("missing child policy %q:\n%s", want, child)
		}
	}
	if strings.Contains(child, "skuid 0") || strings.Contains(parent, "*") {
		t.Fatal("broad UID/link bypass")
	}
	var routes, up bool
	for _, c := range r.calls {
		joined := strings.Join(c.args, " ")
		routes = routes || strings.Contains(joined, "-6 route add default dev eth0")
		up = up || strings.Contains(joined, "link set lo up")
	}
	if !routes || !up {
		t.Fatal("missing IPv6 route or loopback")
	}
}

func TestFailedPreparationNeverReadyAndCleansPartialState(t *testing.T) {
	for _, stage := range []string{"veth", "parent-policy", "child-policy"} {
		t.Run(stage, func(t *testing.T) {
			r := &recorder{}
			h := testHandle(t, r)
			failure := errors.New("setup sentinel")
			r.fail = func(c invocation) error {
				if stage == "veth" && strings.Contains(strings.Join(c.args, " "), " link add ") || stage == "parent-policy" && c.tool == nftTool && c.input != "" || stage == "child-policy" && c.tool == nsenterTool && c.input != "" {
					return failure
				}
				return nil
			}
			err := h.prepare(context.Background())
			if !errors.Is(err, failure) || errors.Is(err, ErrCleanupUnconfirmed) || h.ready {
				t.Fatalf("ready=%v err=%v", h.ready, err)
			}
			found := false
			for _, c := range r.calls {
				found = found || reflect.DeepEqual(c.args, []string{"netns", "delete", h.name})
			}
			if !found {
				t.Fatal("partial namespace not cleaned")
			}
			if _, err := os.Stat(h.statePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("state remains: %v", err)
			}
		})
	}
}

func TestCleanupAttemptsAllResourcesAndReportsRetryableErrors(t *testing.T) {
	r := &recorder{}
	h := testHandle(t, r)
	if err := h.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	linkFailure := errors.New("link cleanup")
	tableFailure := errors.New("table cleanup")
	r.calls = nil
	r.fail = func(c invocation) error {
		if reflect.DeepEqual(c.args, []string{"link", "delete", h.host}) {
			return linkFailure
		}
		if reflect.DeepEqual(c.args, []string{"delete", "table", "inet", h.table}) {
			return tableFailure
		}
		return nil
	}
	err := h.Close(context.Background())
	if !errors.Is(err, linkFailure) || !errors.Is(err, tableFailure) {
		t.Fatalf("lost errors: %v", err)
	}
	found := false
	for _, c := range r.calls {
		found = found || reflect.DeepEqual(c.args, []string{"netns", "delete", h.name})
	}
	if !found {
		t.Fatal("cleanup stopped before namespace")
	}
	if h.ready {
		t.Fatal("closed handle remains usable")
	}
	r.fail = nil
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(r.calls)
	if err := h.Close(context.Background()); err != nil || len(r.calls) != before {
		t.Fatal("close not idempotent", err)
	}
}

func TestPreparationPreservesCleanupErrorsAndRefusesResidualState(t *testing.T) {
	r := &recorder{}
	h := testHandle(t, r)
	if err := os.Mkdir(h.statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.prepare(context.Background()); err == nil {
		t.Fatal("overwrote residual state")
	}
	if len(r.calls) != 0 {
		t.Fatal("touched network before reserving state")
	}
	if err := os.Remove(h.statePath); err != nil {
		t.Fatal(err)
	}
	setupFailure := errors.New("setup")
	cleanupFailure := errors.New("cleanup")
	r.fail = func(c invocation) error {
		if c.input != "" {
			return setupFailure
		}
		if len(c.args) > 1 && c.args[0] == "netns" && c.args[1] == "delete" {
			return cleanupFailure
		}
		return nil
	}
	err := h.prepare(context.Background())
	if !errors.Is(err, setupFailure) || !errors.Is(err, cleanupFailure) || !errors.Is(err, ErrCleanupUnconfirmed) {
		t.Fatalf("lost prepare/cleanup cause: %v", err)
	}
}
