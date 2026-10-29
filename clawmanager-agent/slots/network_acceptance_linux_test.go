//go:build linux

package slots

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netbroker"
	"github.com/iamlovingit/clawmanager-agent/netrelay"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
)

const slotProbeControl = "cm-only-fake-control-sentinel-acceptance"
const slotProbeBusiness = "--cm-slot-network-business"
const slotProbeAuxiliary = "--cm-slot-network-auxiliary"
const slotProbeOffline = "--cm-slot-network-offline"

// Only the explicitly env-gated Linux acceptance test launches these modes.
// They let the real worker execute this static test binary inside its sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case slotProbeOffline:
			serveOfflineMaintenanceProbe()
			return
		case netrelay.Command:
			_, err := netrelay.RunCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout)
			if err != nil {
				os.Exit(2)
			}
			return
		case slotProbeBusiness:
			if err := serveSlotNetworkProbe(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			return
		case slotProbeAuxiliary:
			namespace, err := os.Readlink("/proc/self/ns/net")
			if err != nil {
				os.Exit(2)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"namespace": namespace})
			return
		}
	}
	os.Exit(m.Run())
}

type slotNetworkProbeProfile struct{ generic.Profile }

// This synthetic Go fixture has no native generated credential files. Real
// runtime profiles must implement their own pre-execution migration.
func (slotNetworkProbeProfile) MigrateManagedCredentials(context.Context, gateway.Config, gateway.CreateGatewayRequest, string) error {
	return nil
}

func (p slotNetworkProbeProfile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewHTTPGatewayHealthChecker(cfg)
}

type slotNetworkProbeResult struct {
	Namespace string          `json:"namespace"`
	Checks    map[string]bool `json:"checks"`
}

func serveSlotNetworkProbe() error {
	port, err := strconv.Atoi(os.Getenv("CLAWMANAGER_GATEWAY_PORT"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	for offset := 1; offset < 3; offset++ {
		secondary, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+offset)))
		if e != nil {
			return e
		}
		defer secondary.Close()
		go http.Serve(secondary, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secondary-ok") }))
	}
	return http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		result := slotNetworkProbeResult{Checks: map[string]bool{}}
		result.Namespace, _ = os.Readlink("/proc/self/ns/net")
		hosts, err := os.ReadFile("/etc/hosts")
		result.Checks["local_hosts"] = err == nil && strings.Contains(string(hosts), "clawmanager-local-model.invalid") && strings.Contains(string(hosts), "clawmanager-local-services.invalid")
		result.Checks["no_management_env"] = true
		for _, value := range os.Environ() {
			if strings.Contains(value, slotProbeControl) || strings.Contains(value, "v1.probe-") {
				result.Checks["no_management_env"] = false
			}
		}
		_, err = os.ReadFile("/tmp/cm-slot-A-sentinel")
		result.Checks["fresh_tmp"] = os.IsNotExist(err)
		if os.Getenv("CLAWMANAGER_INSTANCE_ID") == "101" {
			err := os.WriteFile("/tmp/cm-slot-A-sentinel", []byte("A-only"), 0600)
			data, readErr := os.ReadFile("/tmp/cm-slot-A-sentinel")
			result.Checks["tmp_created"] = err == nil && readErr == nil && string(data) == "A-only"
		} else {
			result.Checks["tmp_created"] = true
		}
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, err := client.Post("http://clawmanager-local-model.invalid:31281/api/v1/gateway/llm/v1/chat/completions", "application/json", strings.NewReader("{}"))
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			response.Body.Close()
			result.Checks["model_broker"] = response.StatusCode == 200 && string(data) == "model-ok"
		}
		response, err = client.Get("http://clawmanager-local-services.invalid:31282/json/version")
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			response.Body.Close()
			result.Checks["services_broker"] = response.StatusCode == 200 && string(data) == "service-ok"
		}
		conn, err := net.DialTimeout("tcp", "8.8.8.8:443", 3*time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			data := make([]byte, 6)
			_, err = io.ReadFull(conn, data)
			conn.Close()
			result.Checks["transparent_tcp"] = err == nil && string(data) == "tcp-ok"
		}
		conn, err = net.DialTimeout("tcp", "192.168.9.9:443", time.Second)
		if err != nil {
			result.Checks["private_denied"] = true
		} else {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			data := make([]byte, 1)
			n, readErr := conn.Read(data)
			conn.Close()
			result.Checks["private_denied"] = n == 0 && readErr == io.EOF
		}
		conn, err = net.DialTimeout("tcp", "127.0.0.1:28444", time.Second)
		result.Checks["outer_loopback_denied"] = err != nil
		if conn != nil {
			conn.Close()
		}
		conn, err = net.DialTimeout("unix", "@cm-slot-acceptance-private", time.Second)
		result.Checks["outer_abstract_denied"] = err != nil
		if conn != nil {
			conn.Close()
		}
		conn, err = net.DialTimeout("tcp", "clawmanager-local-services.invalid:31283", time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, writeErr := io.WriteString(conn, "*1\r\n$4\r\nPING\r\n")
			line, e := bufio.NewReader(conn).ReadString('\n')
			conn.Close()
			result.Checks["redis_broker"] = writeErr == nil && e == nil && line == "+PONG\r\n"
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
}

// Run only in a disposable userns Pod with the reviewed trusted-slot caps,
// iproute2/nftables/bwrap, no business PVC/Secret/SA or host namespace mounts.
func TestProtectedSlotLinuxAcceptance(t *testing.T) {
	if os.Getenv("TEST_PROTECTED_SLOT_DISPOSABLE") != "1" {
		t.Skip("requires disposable Linux namespace acceptance Pod")
	}
	if os.Geteuid() != 0 {
		t.Fatal("trusted userns root required")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := "/workspaces/cm-slot-network-acceptance"
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal("fresh disposable workspace required:", err)
	}
	var expiring atomic.Bool
	var privateDenials atomic.Int32
	directHits := slotProbePrivateControl(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runtime/security/lease":
			if r.Header.Get(gateway.ControlTokenHeader) != slotProbeControl {
				w.WriteHeader(403)
				return
			}
			var id netbroker.Identity
			if json.NewDecoder(r.Body).Decode(&id) != nil {
				w.WriteHeader(400)
				return
			}
			ttl := 2 * time.Minute
			if expiring.Load() {
				ttl = 4 * time.Second
			}
			_ = json.NewEncoder(w).Encode(netbroker.Lease{Identity: id, ModelToken: "v1.probe-model", EgressToken: "v1.probe-egress", ServicesToken: "v1.probe-services", ExpiresAt: time.Now().Add(ttl)})
		case "/api/v1/gateway/llm/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer v1.probe-model" {
				w.WriteHeader(403)
				return
			}
			_, _ = io.WriteString(w, "model-ok")
		case "/api/v1/runtime/services/json/version":
			if r.Header.Get("Authorization") != "Bearer v1.probe-services" {
				w.WriteHeader(403)
				return
			}
			_, _ = io.WriteString(w, "service-ok")
		case "/api/v1/runtime/services/team/redis":
			if r.Header.Get("Authorization") != "Bearer v1.probe-services" {
				w.WriteHeader(403)
				return
			}
			conn, buffered, e := http.NewResponseController(w).Hijack()
			if e != nil {
				return
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: clawmanager-redis\r\n\r\n")
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			expected := "*1\r\n$4\r\nPING\r\n"
			command := make([]byte, len(expected))
			if _, e := io.ReadFull(buffered.Reader, command); e != nil || string(command) != expected {
				t.Error("RESP bridge did not deliver exact PING before reply")
				return
			}
			_, _ = io.WriteString(conn, "+PONG\r\n")
			_, _ = io.Copy(io.Discard, buffered.Reader)
		default:
			w.WriteHeader(404)
		}
	}))
	defer backend.Close()
	egress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") == "Bearer v1.probe-egress" && r.Method == "CONNECT" && r.Host == "192.168.9.9:443" {
			privateDenials.Add(1)
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Bearer v1.probe-egress" || r.Method != "CONNECT" || r.Host != "8.8.8.8:443" {
			w.WriteHeader(403)
			return
		}
		conn, buffered, e := http.NewResponseController(w).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\ntcp-ok")
		_, _ = io.Copy(io.Discard, buffered.Reader)
	}))
	defer egress.Close()
	outer, err := net.Listen("tcp", "127.0.0.1:28444")
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	abstract, err := net.Listen("unix", "@cm-slot-acceptance-private")
	if err != nil {
		t.Fatal(err)
	}
	defer abstract.Close()
	cfg := gateway.Config{RuntimeType: "opencode", Runtime: slotNetworkProbeProfile{generic.NewProfile("opencode")}, ControlToken: slotProbeControl, WorkspaceRoot: base, AgentDataDir: filepath.Join(base, "management"), Capacity: 1, GatewayPortStart: 28080, GatewayPortEnd: 28082, GatewayPortBlockSize: 3, GatewayCommand: []string{executable, slotProbeBusiness}, GatewayStartupTimeout: 12 * time.Second, ProcessStopTimeout: 2 * time.Second, BackendURL: backend.URL, PodUID: "slot-acceptance-pod", NetworkSecurityProfile: "isolated-network-v1", NetworkSlotIndex: 0, NetworkSlotCount: 16, NetworkEgressProxyURL: egress.URL}
	worker, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := worker.Shutdown(ctx); err != nil {
			t.Errorf("writer cleanup unconfirmed; evidence retained: %v", err)
			return
		}
		_ = os.RemoveAll(base)
	})
	call := func(method, path string, value any) []byte {
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set(gateway.ControlTokenHeader, slotProbeControl)
		out := httptest.NewRecorder()
		worker.ServeHTTP(out, r)
		if out.Code < 200 || out.Code >= 300 {
			t.Fatalf("control %s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		return out.Body.Bytes()
	}
	getProbe := func() slotNetworkProbeResult {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			response, e := (&http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}).Get("http://127.0.0.1:28080/probe")
			if e == nil {
				var result slotNetworkProbeResult
				e = json.NewDecoder(response.Body).Decode(&result)
				response.Body.Close()
				if e == nil && result.Namespace != "" {
					return result
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("business gateway unavailable: %s", call("GET", "/v1/slot-state", nil))
		return slotNetworkProbeResult{}
	}
	var previousNamespace string
	for _, id := range []int{101, 102} {
		beforePrivate := privateDenials.Load()
		req := gateway.CreateGatewayRequest{InstanceID: id, UserID: 1, Generation: 1, AgentType: "opencode", PodUID: cfg.PodUID, SecurityProfile: cfg.NetworkSecurityProfile, UID: 200000 + id, GID: 200000 + id, GatewayPort: 28080, WorkspacePath: filepath.Join(base, "opencode", "user-1", fmt.Sprintf("instance-%d", id)), Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_" + strings.Repeat(strconv.Itoa(id), 16)}}
		call("POST", "/v1/gateways", req)
		result := getProbe()
		if privateDenials.Load() <= beforePrivate || directHits.Load() != 0 {
			t.Fatal("private target denial lacked proxy evidence or hit reachable outer target")
		}
		for _, port := range []int{28081, 28082} {
			response, e := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}).Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
			if e != nil {
				t.Fatal("secondary port ingress failed", e)
			}
			body, e := io.ReadAll(io.LimitReader(response.Body, 100))
			response.Body.Close()
			if e != nil || response.StatusCode != 200 || string(body) != "secondary-ok" {
				t.Fatal("secondary native ingress response mismatch")
			}
		}
		for _, check := range []string{"local_hosts", "no_management_env", "fresh_tmp", "tmp_created", "model_broker", "services_broker", "transparent_tcp", "private_denied", "outer_loopback_denied", "outer_abstract_denied", "redis_broker"} {
			if !result.Checks[check] {
				t.Errorf("instance %d: %s failed", id, check)
			}
		}
		if result.Namespace == previousNamespace {
			t.Fatal("slot reused previous network namespace")
		}
		previousNamespace = result.Namespace
		spec := gateway.GatewayStartSpec{GatewayID: fmt.Sprintf("gw-%d-1", id), RuntimeType: "opencode", InstanceID: id, UserID: 1, Generation: 1, UID: req.UID, GID: req.GID, WorkspacePath: req.WorkspacePath, Port: 28080, Command: []string{executable, slotProbeAuxiliary}, Env: cfg.Runtime.GatewayEnv(nil, cfg, req, req.WorkspacePath, 28080)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, e := (sandboxStarter{cfg: worker.cfg, network: worker.network}).RunGatewayCommand(ctx, spec)
		cancel()
		var auxiliary map[string]string
		if e != nil || json.Unmarshal(output, &auxiliary) != nil || auxiliary["namespace"] != result.Namespace {
			t.Fatalf("auxiliary did not share its own execution network: %v %s", e, output)
		}
		call("DELETE", "/v1/gateways/"+spec.GatewayID, nil)
		entries, e := os.ReadDir(filepath.Join(cfg.AgentDataDir, "network"))
		if e != nil || len(entries) != 0 {
			t.Fatalf("network ownership not fully cleaned: %v %v", entries, e)
		}
	}
	expiring.Store(true)
	fault := gateway.CreateGatewayRequest{InstanceID: 103, UserID: 1, Generation: 1, AgentType: "opencode", PodUID: cfg.PodUID, SecurityProfile: cfg.NetworkSecurityProfile, UID: 200103, GID: 200103, GatewayPort: 28080, WorkspacePath: filepath.Join(base, "opencode", "user-1", "instance-103"), Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_" + strings.Repeat("fault", 10)}}
	call("POST", "/v1/gateways", fault)
	_ = getProbe()
	deadline := time.Now().Add(9 * time.Second)
	quarantined := false
	for time.Now().Before(deadline) {
		var snapshot Snapshot
		if json.Unmarshal(call("GET", "/v1/slot-state", nil), &snapshot) != nil {
			t.Fatal("invalid state")
		}
		if snapshot.Quarantined {
			quarantined = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !quarantined {
		t.Fatal("expired management credentials did not quarantine execution")
	}
	fault.InstanceID, fault.UID, fault.GID = 104, 200104, 200104
	fault.WorkspacePath = filepath.Join(base, "opencode", "user-1", "instance-104")
	data, _ := json.Marshal(fault)
	request := httptest.NewRequest("POST", "/v1/gateways", bytes.NewReader(data))
	request.Header.Set(gateway.ControlTokenHeader, slotProbeControl)
	denied := httptest.NewRecorder()
	worker.ServeHTTP(denied, request)
	if denied.Code != http.StatusConflict {
		t.Fatalf("failed execution reused without stop proof: %d", denied.Code)
	}
	call("DELETE", "/v1/gateways/gw-103-1", nil)
	t.Log("actual multiport slot primary+auxiliary, local model/services/RESP, transparent TCP, private/localhost/abstract isolation, fresh A-to-B tmp/network, lease-expiry quarantine and confirmed cleanup passed")
}

// A reachable private destination distinguishes policy enforcement from a
// missing route/server. This address belongs only to the disposable Pod NET.
func slotProbePrivateControl(t *testing.T) *atomic.Int32 {
	t.Helper()
	if output, err := exec.Command("ip", "address", "add", "192.168.9.9/32", "dev", "lo").CombinedOutput(); err != nil {
		t.Fatalf("private target address not freshly owned: %v %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("ip", "address", "del", "192.168.9.9/32", "dev", "lo").CombinedOutput(); err != nil {
			t.Errorf("private target address cleanup: %v %s", err, output)
		}
	})
	listener, err := net.Listen("tcp", "192.168.9.9:443")
	if err != nil {
		t.Fatal(err)
	}
	hits := new(atomic.Int32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			hits.Add(1)
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(conn, "private-leak")
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	conn, err := net.DialTimeout("tcp", "192.168.9.9:443", time.Second)
	if err != nil {
		t.Fatal("private positive control unreachable", err)
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	data := make([]byte, len("private-leak"))
	_, err = io.ReadFull(conn, data)
	conn.Close()
	if err != nil || string(data) != "private-leak" || hits.Load() != 1 {
		t.Fatal("private positive control failed")
	}
	hits.Store(0)
	return hits
}
