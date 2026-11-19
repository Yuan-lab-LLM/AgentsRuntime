//go:build linux

package slots

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netbroker"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
	"github.com/iamlovingit/clawmanager-agent/sandboxprofile"
)

// This checks the production startup path, including native preparation and
// Tracker's proc/listener attribution. The containing disposable Pod must still
// be destroyed after the test; primary exit is deliberately not writer proof.
func TestRootlessProductionSlotAcceptance(t *testing.T) {
	if os.Getenv("CM_ROOTLESS_SLOT_ACCEPTANCE") != "1" {
		t.Skip("requires disposable ordinary Pod and provisioned management directories")
	}
	if os.Geteuid() != sandboxprofile.ManagementUID {
		t.Fatal("management UID required")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("CM_ROOTLESS_SLOT_BASE")
	if base == "" {
		base = "/workspaces/cm-rootless-slot-acceptance"
	}
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
			_ = json.NewEncoder(w).Encode(netbroker.Lease{Identity: id, ModelToken: "v1.probe-model", EgressToken: "v1.probe-egress", ServicesToken: "v1.probe-services", ExpiresAt: time.Now().Add(2 * time.Minute)})
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
			c, b, e := http.NewResponseController(w).Hijack()
			if e != nil {
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(4 * time.Second))
			_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: clawmanager-redis\r\n\r\n")
			command := make([]byte, len("*1\r\n$4\r\nPING\r\n"))
			if _, e = io.ReadFull(b.Reader, command); e != nil || string(command) != "*1\r\n$4\r\nPING\r\n" {
				return
			}
			_, _ = io.WriteString(c, "+PONG\r\n")
		default:
			w.WriteHeader(404)
		}
	}))
	defer backend.Close()
	egress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Bearer v1.probe-egress" || r.Method != "CONNECT" || r.Host != "8.8.8.8:443" {
			w.WriteHeader(403)
			return
		}
		c, b, e := http.NewResponseController(w).Hijack()
		if e != nil {
			return
		}
		defer c.Close()
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\ntcp-ok")
		_, _ = io.Copy(io.Discard, b.Reader)
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
	cfg := gateway.Config{RuntimeType: "opencode", Runtime: slotNetworkProbeProfile{generic.NewProfile("opencode")}, SandboxProfile: sandboxprofile.NFSRootlessV1, IsolatedLifecycle: true, GatewayBaseEnv: []string{}, ControlToken: slotProbeControl, WorkspaceRoot: base, AgentDataDir: filepath.Join(base, "management"), Capacity: 1, GatewayPortStart: 28080, GatewayPortEnd: 28082, GatewayPortBlockSize: 3, GatewayStartupTimeout: 12 * time.Second, ProcessStopTimeout: 2 * time.Second, BackendURL: backend.URL, PodUID: "rootless-slot-acceptance", NetworkSecurityProfile: "isolated-network-v1", NetworkSlotIndex: 0, NetworkSlotCount: 2, NetworkEgressProxyURL: egress.URL}
	if err = os.Mkdir(cfg.AgentDataDir, 0700); err != nil {
		t.Fatal("fresh provisioned management workspace required:", err)
	}
	req := gateway.CreateGatewayRequest{InstanceID: 101, UserID: 1, Generation: 1, AgentType: "opencode", PodUID: cfg.PodUID, SecurityProfile: cfg.NetworkSecurityProfile, UID: 200101, GID: 200101, GatewayPort: 28080, WorkspacePath: filepath.Join(base, "opencode", "user-1", "instance-101"), Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_" + strings.Repeat("probe", 12), "CM_PROBE_MANAGER_PID": strconv.Itoa(os.Getpid())}}
	if err = PrepareInstanceStorage(cfg, req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	starter := sandboxStarter{cfg: cfg, network: &networkOwner{}}
	release, err := starter.PrepareGateway(ctx, req, 28080)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := release(cleanup); err != nil {
			t.Error(err)
		}
	}()
	if err = starter.PrepareNativeStorage(ctx, req, 28080); err != nil {
		t.Fatal("native preparation:", err)
	}
	spec := gateway.GatewayStartSpec{GatewayID: "gw-101-1", RuntimeType: "opencode", InstanceID: 101, UserID: 1, Generation: 1, UID: req.UID, GID: req.GID, WorkspacePath: req.WorkspacePath, Port: 28080, Command: []string{executable, slotProbeBusiness}, Env: cfg.Runtime.GatewayEnv(cfg.GatewayBaseEnv, cfg, req, req.WorkspacePath, 28080)}
	process, err := starter.StartGateway(ctx, spec)
	if err != nil {
		t.Fatal("primary startup:", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.PrepareReset(cleanup, false); err != nil {
			t.Error("primary reset preparation:", err)
		}
	}()
	var result slotNetworkProbeResult
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		response, e := client.Get("http://127.0.0.1:28080/probe")
		if e == nil {
			e = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if e == nil && result.Namespace != "" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if result.Namespace == "" {
		t.Fatal("primary HTTP ingress unavailable")
	}
	for _, name := range []string{"local_hosts", "no_management_env", "management_proc_denied", "dns_broker", "fresh_tmp", "tmp_created", "model_broker", "services_broker", "transparent_tcp", "private_denied", "outer_loopback_denied", "outer_abstract_denied", "redis_broker"} {
		if !result.Checks[name] {
			t.Errorf("%s failed", name)
		}
	}
	if err = process.VerifyListeners(ctx); err != nil {
		t.Fatal("real listener attribution:", err)
	}

	if !starter.GatewayPreviewAvailable(spec) {
		t.Fatal("rootless preview transport unavailable")
	}
	preview, err := starter.DialGatewayPreview(ctx, spec, "tcp", "127.0.0.1:3000")
	if err != nil {
		t.Fatal("rootless preview dial:", err)
	}
	defer preview.Close()
	_ = preview.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(preview, "GET /root%2Fasset?q=a%2Fb HTTP/1.1\r\nHost: localhost:3000\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(preview), nil)
	if err != nil {
		t.Fatal("rootless preview response:", err)
	}
	var previewResult map[string]string
	err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&previewResult)
	response.Body.Close()
	preview.Close()
	if err != nil || response.StatusCode != 200 || previewResult["namespace"] != result.Namespace || previewResult["instance"] != "101" || previewResult["uri"] != "/root%2Fasset?q=a%2Fb" {
		t.Fatalf("rootless preview scope mismatch: status=%d result=%v err=%v", response.StatusCode, previewResult, err)
	}
	for _, port := range []int{28081, 28082} {
		response, e := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || string(body) != "secondary-ok" {
			t.Fatal("secondary ingress mismatch", e, string(body))
		}
	}
	spec.Command = []string{executable, slotProbeAuxiliary}
	output, err := starter.RunGatewayCommand(ctx, spec)
	if err != nil {
		t.Fatalf("auxiliary: %v %s", err, output)
	}
	var auxiliary map[string]string
	if json.NewDecoder(bufio.NewReader(strings.NewReader(string(output)))).Decode(&auxiliary) != nil || auxiliary["namespace"] != result.Namespace {
		t.Fatalf("auxiliary network mismatch: %s", output)
	}
	t.Log("production native preparation, primary, proc/listener attribution, auxiliary, private preview, model/services/Redis, transparent TCP and private ingress passed with outer SETUID/SETGID only")
}
