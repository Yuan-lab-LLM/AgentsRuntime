package process

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

func TestDeepSeekReadinessUsesConfiguredAuthenticatedProxy(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("DSH readiness path=%q", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	// An inherited OpenClaw image version must not add OpenClaw-specific probes.
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.10.2")
	m := New(appconfig.Config{RuntimeType: "deepseek-harness", OpenClawHealthURL: server.URL + "/"})
	if !m.checkHealth() {
		t.Fatal("authenticated DSH proxy is not ready")
	}
	for _, code := range []int{401, 403, 404, 502} {
		status = code
		if m.checkHealth() {
			t.Fatalf("HTTP %d was treated as healthy", code)
		}
	}
	m.cfg.OpenClawHealthURL = ""
	if m.checkHealth() {
		t.Fatal("missing DSH probe was treated as healthy")
	}
}

func TestDeepSeekDoesNotStartOpenClawModelWarmup(t *testing.T) {
	m := New(appconfig.Config{RuntimeType: "deepseek-harness", OpenClawDoctorPolicy: "auto"})
	if m.doctorPolicy() != "never" {
		t.Fatal("existing Pro config enabled OpenClaw doctor")
	}
	m.startGatewayModelsWarmup(make(chan struct{}))
	if m.warmupStarted || m.warmupDone {
		t.Fatal("DSH started OpenClaw warmup")
	}
}

func TestDeepSeekStartupTimeoutTerminatesUnreadyProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestDeepSeekSlowChild")
	cmd.Env = append(os.Environ(), "DSH_TEST_SLOW_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	m := New(appconfig.Config{RuntimeType: "deepseek-harness", OpenClawStartupHealthTimeout: time.Millisecond})
	m.cmd, m.status = cmd, StatusStarting
	m.promoteRunning(cmd.Process.Pid, false)
	if err := cmd.Wait(); err == nil {
		t.Fatal("unready child was not terminated")
	}
	if m.status == StatusRunning {
		t.Fatal("unready DSH was promoted to running")
	}
}

func TestDeepSeekSlowChild(t *testing.T) {
	if os.Getenv("DSH_TEST_SLOW_CHILD") == "1" {
		time.Sleep(time.Minute)
	}
}
