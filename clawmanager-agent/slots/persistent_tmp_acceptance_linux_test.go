//go:build linux

package slots

import (
	"bytes"
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
	"syscall"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
)

const persistentTmpBusiness = "--cm-instance-tmp-fixture"
const persistentTmpControl = "cm-instance-tmp-disposable-control"
const persistentTmpPayload = "instance-A-persistent-bytes"

// This fixture mode is reachable only with an explicit argv in a disposable
// Linux test binary. It performs no model, Redis, browser or platform calls.
func init() {
	if len(os.Args) > 1 && os.Args[1] == persistentTmpBusiness {
		if err := servePersistentTmpFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
}

type persistentTmpSnapshot struct {
	A, B                                                             string
	Mode                                                             uint32
	UID, GID                                                         uint32
	MTime                                                            int64
	FreshRun, FreshShm, HiddenParents, ReadonlyRoot, NoManagementEnv bool
}

func servePersistentTmpFixture() error {
	port, err := strconv.Atoi(os.Getenv("CLAWMANAGER_GATEWAY_PORT"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			io.WriteString(w, "ok")
			return
		}
		if r.URL.Path == "/write" && r.Method == http.MethodPost {
			actor := os.Getenv("CM_TMP_ACTOR")
			if actor != "A" && actor != "B" {
				http.Error(w, "invalid actor", 400)
				return
			}
			name := "/tmp/cm-instance-" + actor + ".txt"
			data := "instance-B-persistent-bytes"
			if actor == "A" {
				data = persistentTmpPayload
			}
			if err := os.WriteFile(name, []byte(data), 0644); err != nil {
				http.Error(w, "tmp write failed", 500)
				return
			}
			if err := os.Chmod(name, 0644); err != nil {
				http.Error(w, "tmp chmod failed", 500)
				return
			}
			when := time.Unix(1700000000, 0)
			if err := os.Chtimes(name, when, when); err != nil {
				http.Error(w, "tmp timestamp failed", 500)
				return
			}
			for _, p := range []string{"/run/cm-instance-runtime", "/dev/shm/cm-instance-runtime"} {
				if err := os.WriteFile(p, []byte(actor), 0600); err != nil {
					http.Error(w, "runtime scratch failed", 500)
					return
				}
			}
			io.WriteString(w, "written")
			return
		}
		if r.URL.Path != "/snapshot" {
			http.NotFound(w, r)
			return
		}
		a, _ := os.ReadFile("/tmp/cm-instance-A.txt")
		b, _ := os.ReadFile("/tmp/cm-instance-B.txt")
		snap := persistentTmpSnapshot{A: string(a), B: string(b), HiddenParents: true, NoManagementEnv: os.Getenv("RUNTIME_AGENT_CONTROL_TOKEN") == ""}
		_, e := os.Stat("/run/cm-instance-runtime")
		snap.FreshRun = os.IsNotExist(e)
		_, e = os.Stat("/dev/shm/cm-instance-runtime")
		snap.FreshShm = os.IsNotExist(e)
		for _, p := range []string{"/workspaces/.clawmanager-slot-locks", "/workspaces/.clawmanager-instance-tmp", "/workspaces/opencode/user-1/instance-101/cm-private-control"} {
			if _, e = os.Stat(p); !os.IsNotExist(e) {
				snap.HiddenParents = false
			}
		}
		if f, e := os.OpenFile("/usr/cm-instance-tmp-forbidden", os.O_CREATE|os.O_WRONLY, 0600); e != nil {
			snap.ReadonlyRoot = true
		} else {
			f.Close()
		}
		if info, e := os.Stat("/tmp/cm-instance-A.txt"); e == nil {
			snap.Mode = uint32(info.Mode().Perm())
			snap.MTime = info.ModTime().Unix()
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				snap.UID, snap.GID = stat.Uid, stat.Gid
			}
		}
		json.NewEncoder(w).Encode(snap)
	}))
}

type persistentTmpProfile struct{ generic.Profile }

func (p persistentTmpProfile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return gateway.NewHTTPGatewayHealthChecker(cfg)
}

// Run source and reuse-B in one Pod, then target in another Pod sharing the
// same explicitly owned test PVC. No existing user instance or pool is touched.
func TestLinuxPersistentTmpAcrossPods(t *testing.T) {
	if os.Getenv("CM_PERSISTENT_TMP_ACCEPTANCE") != "1" {
		t.Skip("explicit owned Linux PVC fixture only")
	}
	if os.Getuid() != 0 {
		t.Fatal("requires trusted disposable supervisor UID")
	}
	root := os.Getenv("CM_PERSISTENT_TMP_ROOT")
	phase := os.Getenv("CM_PERSISTENT_TMP_PHASE")
	if root != "/workspaces" || (phase != "source" && phase != "reuse-B" && phase != "target") {
		t.Fatal("invalid disposable acceptance scope")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(executable, "/usr/local/bin/cm-instance-tmp") {
		t.Fatal("fixture binary must be immutable sandbox image path")
	}
	dataRoot, err := os.MkdirTemp("/tmp", "cm-instance-tmp-manager-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataRoot)
	podUID := os.Getenv("CM_PERSISTENT_TMP_POD_UID")
	if podUID == "" {
		t.Fatal("actual fixture Pod UID required")
	}
	cfg := gateway.Config{RuntimeType: "opencode", Runtime: persistentTmpProfile{generic.NewProfile("opencode")}, ControlToken: persistentTmpControl, WorkspaceRoot: root, AgentDataDir: dataRoot, Capacity: 1, GatewayPortStart: 28080, GatewayPortEnd: 28080, GatewayPortBlockSize: 1, GatewayCommand: []string{executable, persistentTmpBusiness}, GatewayStartupTimeout: 12 * time.Second, ProcessStopTimeout: 2 * time.Second, PodUID: podUID}
	worker, err := NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := worker.Shutdown(ctx); e != nil {
			t.Errorf("unconfirmed fixture stop: %v", e)
		}
	}()
	actor, id, generation := "A", 101, 1
	if phase == "reuse-B" {
		actor, id = "B", 102
	}
	if phase == "target" {
		generation = 2
	}
	req := gateway.CreateGatewayRequest{InstanceID: id, UserID: 1, Generation: generation, AgentType: cfg.RuntimeType, PodUID: cfg.PodUID, UID: 200000 + id, GID: 200000 + id, GatewayPort: 28080, WorkspacePath: filepath.Join(root, "opencode", "user-1", fmt.Sprintf("instance-%d", id)), Environment: map[string]string{"CM_TMP_ACTOR": actor}}
	control := func(method, path string, value any) (int, []byte) {
		data, _ := json.Marshal(value)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set(gateway.ControlTokenHeader, persistentTmpControl)
		w := httptest.NewRecorder()
		worker.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	status, body := control(http.MethodPost, "/v1/gateways", req)
	if status != 200 && status != 201 && status != 202 {
		t.Fatalf("create: %d %s", status, body)
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	snapshot := func() persistentTmpSnapshot {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			response, e := client.Get("http://127.0.0.1:28080/snapshot")
			if e == nil {
				var s persistentTmpSnapshot
				e = json.NewDecoder(response.Body).Decode(&s)
				response.Body.Close()
				if e == nil {
					return s
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		_, state := control("GET", "/v1/slot-state", nil)
		t.Fatalf("fixture not ready: %s", state)
		return persistentTmpSnapshot{}
	}
	first := snapshot()
	if !first.FreshRun || !first.FreshShm || !first.HiddenParents || !first.ReadonlyRoot || !first.NoManagementEnv {
		t.Fatalf("initial isolation: %+v", first)
	}
	if phase == "target" {
		if first.A != persistentTmpPayload || first.B != "" || first.Mode != 0644 || first.UID != 200101 || first.GID != 200101 || first.MTime != 1700000000 {
			t.Fatalf("same-instance data/metadata not restored: %+v", first)
		}
	} else {
		if first.A != "" || first.B != "" {
			t.Fatalf("foreign or stale tmp exposed: %+v", first)
		}
	}
	if phase != "target" {
		response, e := client.Post("http://127.0.0.1:28080/write", "application/octet-stream", nil)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("write failed")
		}
		after := snapshot()
		if actor == "A" && after.A != persistentTmpPayload {
			t.Fatal("A write not readable")
		}
		if actor == "B" && (after.A != "" || after.B != "instance-B-persistent-bytes") {
			t.Fatal("B tmp isolation failed")
		}
	}
	// Auxiliary commands must see the same instance tmp without receiving the
	// trusted parent paths, even though they use another mount namespace.
	spec := gateway.GatewayStartSpec{GatewayID: fmt.Sprintf("gw-%d-%d", id, generation), RuntimeType: cfg.RuntimeType, InstanceID: id, UserID: 1, Generation: generation, UID: req.UID, GID: req.GID, WorkspacePath: req.WorkspacePath, Port: 28080, Command: []string{"/bin/cat", "/tmp/cm-instance-" + actor + ".txt"}, Env: cfg.Runtime.GatewayEnv(nil, cfg, req, req.WorkspacePath, 28080)}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	output, e := (sandboxStarter{cfg: worker.cfg, network: worker.network}).RunGatewayCommand(ctx, spec)
	cancel()
	if e != nil || string(output) != map[string]string{"A": persistentTmpPayload, "B": "instance-B-persistent-bytes"}[actor] {
		t.Fatalf("auxiliary tmp mismatch: %v %s", e, output)
	}
	status, body = control(http.MethodDelete, "/v1/gateways/"+spec.GatewayID, nil)
	if status != 200 {
		t.Fatalf("stop: %d %s", status, body)
	}
	var receipt map[string]any
	if json.Unmarshal(body, &receipt) != nil || receipt["confirmed"] != true || receipt["unmounted"] != true {
		t.Fatal("stop receipt not confirmed")
	}
	if phase == "source" {
		status, _ = control(http.MethodPost, "/v1/gateways", req)
		if status != 409 {
			t.Fatal("late same-generation create bypassed writer fence")
		}
	}
	t.Logf("PASS phase=%s actual_pod_uid=%s instance=%d generation=%d stop_confirmed=true auxiliary_same_tmp=true", phase, podUID, id, generation)
}
