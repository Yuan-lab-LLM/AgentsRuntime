package hermes_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/runtime/hermes"
)

func TestProfileDefaults(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	if profile.Type() != "hermes" {
		t.Fatalf("Type() = %q, want hermes", profile.Type())
	}
	if profile.DisplayName() != "Hermes" {
		t.Fatalf("DisplayName() = %q, want Hermes", profile.DisplayName())
	}
	defaults := profile.Defaults()
	if defaults.WorkspaceRoot != "/workspaces" {
		t.Fatalf("WorkspaceRoot = %q, want /workspaces", defaults.WorkspaceRoot)
	}
	if defaults.GatewayPortStart != 20000 || defaults.GatewayPortEnd != 20099 {
		t.Fatalf("port range = %d-%d, want 20000-20099", defaults.GatewayPortStart, defaults.GatewayPortEnd)
	}
	if defaults.GatewayPortBlockSize != 1 {
		t.Fatalf("GatewayPortBlockSize = %d, want 1", defaults.GatewayPortBlockSize)
	}
	if defaults.GatewayCapacity != 100 {
		t.Fatalf("GatewayCapacity = %d, want 100", defaults.GatewayCapacity)
	}
	if defaults.GatewayAuthMode != "trusted-proxy" {
		t.Fatalf("GatewayAuthMode = %q, want trusted-proxy", defaults.GatewayAuthMode)
	}
}

func TestGatewayCommandStartsHermesGateway(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	command := strings.Join(profile.GatewayCommand("trusted-proxy"), " ")
	if command != "start-hermes-dashboard-gateway" {
		t.Fatalf("GatewayCommand() = %q, want start-hermes-dashboard-gateway", command)
	}
}

func TestGatewayEnvSetsHermesWorkspace(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	req := gateway.CreateGatewayRequest{
		AgentType:  "hermes",
		InstanceID: 63,
		UserID:     45,
		Generation: 7,
		Environment: map[string]string{
			"CLAWMANAGER_LLM_API_KEY":      "secret",
			"CLAWMANAGER_TEAM_ENABLED":     "true",
			"CLAWMANAGER_TEAM_ID":          "team-1",
			"CLAWMANAGER_TEAM_MEMBER_ID":   "leader",
			"CLAWMANAGER_TEAM_CONFIG_JSON": `{"teamId":"team-1","memberId":"leader"}`,
			"CLAWMANAGER_TEAM_SHARED_DIR":  "/team",
			"CLAWMANAGER_TEAM_READY_FILE":  "/tmp/untrusted-ready.json",
			"CUSTOM_RUNTIME_ENV":           "forwarded",
		},
	}
	workspacePath := "/workspaces/hermes/user-45/instance-63"
	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "trusted-proxy"}, req, workspacePath, 20017)
	values := envMap(env)
	if values["HOME"] != workspacePath+"/home" {
		t.Fatalf("HOME = %q", values["HOME"])
	}
	if values["HERMES_HOME"] != workspacePath+"/home/.hermes" {
		t.Fatalf("HERMES_HOME = %q", values["HERMES_HOME"])
	}
	if values["HOST"] != "0.0.0.0" || values["PORT"] != "20017" {
		t.Fatalf("host/port env = %#v", values)
	}
	if values["HERMES_ACCEPT_HOOKS"] != "1" {
		t.Fatalf("HERMES_ACCEPT_HOOKS = %q, want 1", values["HERMES_ACCEPT_HOOKS"])
	}
	if values["CLAWMANAGER_LLM_API_KEY"] != "secret" {
		t.Fatalf("CLAWMANAGER_LLM_API_KEY = %q, want secret", values["CLAWMANAGER_LLM_API_KEY"])
	}
	if values["CLAWMANAGER_TEAM_ENABLED"] != "true" {
		t.Fatalf("CLAWMANAGER_TEAM_ENABLED = %q, want true", values["CLAWMANAGER_TEAM_ENABLED"])
	}
	if values["CLAWMANAGER_TEAM_CONFIG_JSON"] != `{"teamId":"team-1","memberId":"leader"}` {
		t.Fatalf("CLAWMANAGER_TEAM_CONFIG_JSON = %q, want request value", values["CLAWMANAGER_TEAM_CONFIG_JSON"])
	}
	if values["CUSTOM_RUNTIME_ENV"] != "forwarded" {
		t.Fatalf("CUSTOM_RUNTIME_ENV = %q, want forwarded", values["CUSTOM_RUNTIME_ENV"])
	}
	if values["CLAWMANAGER_TEAM_CONFIG_PATH"] != workspacePath+"/team/team.json" {
		t.Fatalf("CLAWMANAGER_TEAM_CONFIG_PATH = %q, want workspace Team config", values["CLAWMANAGER_TEAM_CONFIG_PATH"])
	}
	if values["CLAWMANAGER_TEAM_SHARED_DIR"] != workspacePath+"/team" {
		t.Fatalf("CLAWMANAGER_TEAM_SHARED_DIR = %q, want workspace Team directory", values["CLAWMANAGER_TEAM_SHARED_DIR"])
	}
	if values["HERMES_TEAM_WORKER_HOME"] != workspacePath+"/home/.clawmanager-team-worker" {
		t.Fatalf("HERMES_TEAM_WORKER_HOME = %q, want managed private Team home", values["HERMES_TEAM_WORKER_HOME"])
	}
	if values["CLAWMANAGER_TEAM_READY_FILE"] != workspacePath+"/home/.clawmanager-team-worker/.hermes/runtime/redis-team.ready.json" {
		t.Fatalf("CLAWMANAGER_TEAM_READY_FILE = %q, want managed private readiness path", values["CLAWMANAGER_TEAM_READY_FILE"])
	}
	if values["CLAWMANAGER_GATEWAY_GENERATION"] != "7" {
		t.Fatalf("CLAWMANAGER_GATEWAY_GENERATION = %q, want 7", values["CLAWMANAGER_GATEWAY_GENERATION"])
	}
	if values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"] != "clawmanager" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_USERNAME = %q, want clawmanager", values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"])
	}
	if values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] != "secret" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_PASSWORD = %q, want secret from CLAWMANAGER_LLM_API_KEY", values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"])
	}
}

func TestGatewayEnvPrefersInstanceTokenForDashboardBasicAuth(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	req := gateway.CreateGatewayRequest{
		AgentType:  "hermes",
		InstanceID: 70,
		UserID:     45,
		Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "igt_instance_70",
			"CLAWMANAGER_LLM_API_KEY":    "llm-key-should-not-win",
		},
	}
	workspacePath := "/workspaces/hermes/user-45/instance-70"
	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "trusted-proxy"}, req, workspacePath, 20020)
	values := envMap(env)
	if values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"] != "clawmanager" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_USERNAME = %q, want clawmanager", values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"])
	}
	if values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] != "igt_instance_70" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_PASSWORD = %q, want instance token", values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"])
	}
}

func TestGatewayEnvKeepsExplicitHermesDashboardBasicAuth(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	req := gateway.CreateGatewayRequest{
		AgentType:  "hermes",
		InstanceID: 71,
		UserID:     45,
		Environment: map[string]string{
			"HERMES_DASHBOARD_BASIC_AUTH_USERNAME": "admin",
			"HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "explicit-password",
			"CLAWMANAGER_INSTANCE_TOKEN":           "igt_should_not_override",
		},
	}
	workspacePath := "/workspaces/hermes/user-45/instance-71"
	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "trusted-proxy", GatewayToken: "cfg-token"}, req, workspacePath, 20021)
	values := envMap(env)
	if values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"] != "admin" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_USERNAME = %q, want admin", values["HERMES_DASHBOARD_BASIC_AUTH_USERNAME"])
	}
	if values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] != "explicit-password" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_PASSWORD = %q, want explicit-password", values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"])
	}
}

func TestGatewayEnvUsesGatewayTokenBeforeRequestAccessToken(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	req := gateway.CreateGatewayRequest{
		AgentType:  "hermes",
		InstanceID: 72,
		UserID:     45,
		Environment: map[string]string{
			"CLAWMANAGER_INSTANCE_TOKEN": "igt_instance_72",
		},
	}
	workspacePath := "/workspaces/hermes/user-45/instance-72"
	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "token", GatewayToken: "cfg-gateway-token"}, req, workspacePath, 20022)
	values := envMap(env)
	if values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] != "cfg-gateway-token" {
		t.Fatalf("HERMES_DASHBOARD_BASIC_AUTH_PASSWORD = %q, want cfg.GatewayToken", values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"])
	}
}

func TestGatewayEnvMovesDefaultTeamSharedDirIntoWorkspace(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	workspacePath := "/workspaces/hermes/user-45/instance-64"
	req := gateway.CreateGatewayRequest{
		AgentType:  "hermes",
		InstanceID: 64,
		UserID:     45,
		Environment: map[string]string{
			"CLAWMANAGER_TEAM_ENABLED":    "true",
			"CLAWMANAGER_TEAM_SHARED_DIR": "/team",
		},
	}

	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "trusted-proxy"}, req, workspacePath, 20018)
	values := envMap(env)
	if values["CLAWMANAGER_TEAM_SHARED_DIR"] != workspacePath+"/team" {
		t.Fatalf("CLAWMANAGER_TEAM_SHARED_DIR = %q, want workspace Team directory", values["CLAWMANAGER_TEAM_SHARED_DIR"])
	}
	if _, ok := values["CLAWMANAGER_TEAM_CONFIG_PATH"]; ok {
		t.Fatalf("CLAWMANAGER_TEAM_CONFIG_PATH = %q, want unset without config JSON", values["CLAWMANAGER_TEAM_CONFIG_PATH"])
	}
}

func TestGatewayEnvRemovesRuntimePodAgentEnvAndKeepsInstanceAgentEnv(t *testing.T) {
	profile := hermes.NewProfile("hermes")
	workspacePath := "/workspaces/hermes/user-45/instance-65"
	base := []string{
		"RUNTIME_AGENT_CONTROL_TOKEN=control",
		"RUNTIME_AGENT_REPORT_TOKEN=report",
		"RUNTIME_AGENT_DATA_DIR=/var/lib/clawmanager-agent",
		"RUNTIME_AGENT_PUBLIC_PORT=20000",
		"RUNTIME_AGENT_LISTEN_ADDR=127.0.0.1:19090",
		"CLAWMANAGER_AGENT_ENABLED=true",
		"CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN=agt_boot_xxx",
		"CLAWMANAGER_AGENT_INSTANCE_ID=65",
	}
	req := gateway.CreateGatewayRequest{
		InstanceID: 65,
		UserID:     45,
	}

	env := profile.GatewayEnv(base, gateway.Config{RuntimeType: "hermes", GatewayAuthMode: "trusted-proxy"}, req, workspacePath, 20019)
	values := envMap(env)
	for _, key := range []string{
		"RUNTIME_AGENT_CONTROL_TOKEN",
		"RUNTIME_AGENT_REPORT_TOKEN",
		"RUNTIME_AGENT_DATA_DIR",
		"RUNTIME_AGENT_PUBLIC_PORT",
		"RUNTIME_AGENT_LISTEN_ADDR",
	} {
		if _, ok := values[key]; ok {
			t.Fatalf("%s = %q, want unset", key, values[key])
		}
	}
	if values["CLAWMANAGER_AGENT_ENABLED"] != "true" {
		t.Fatalf("CLAWMANAGER_AGENT_ENABLED = %q, want true", values["CLAWMANAGER_AGENT_ENABLED"])
	}
	if values["CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN"] != "agt_boot_xxx" {
		t.Fatalf("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN = %q, want preserved", values["CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN"])
	}
	if values["CLAWMANAGER_AGENT_INSTANCE_ID"] != "65" {
		t.Fatalf("CLAWMANAGER_AGENT_INSTANCE_ID = %q, want 65", values["CLAWMANAGER_AGENT_INSTANCE_ID"])
	}
}

func TestTeamHealthRequiresDashboardAndMatchingConsumerReadiness(t *testing.T) {
	server, port := newHermesHealthServer(t)
	defer server.Close()

	workspace := t.TempDir()
	readyFile := filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "runtime", "redis-team.ready.json")
	spec := teamGatewayStartSpec(workspace, readyFile, port)
	checker := hermes.NewProfile("hermes").HealthChecker(gateway.Config{GatewayStartupTimeout: 180 * time.Millisecond})

	if err := checker.WaitReady(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "consumer readiness") {
		t.Fatalf("WaitReady() error = %v, want missing consumer readiness", err)
	}

	writeStartupState(t, readyFile, map[string]any{
		"ready":      true,
		"state":      "ready",
		"runtime":    "hermes",
		"teamId":     "team-42",
		"memberId":   "developer",
		"instanceId": 63,
		"generation": 7,
	})
	if err := checker.WaitReady(context.Background(), spec); err != nil {
		t.Fatalf("WaitReady() error = %v, want dashboard and consumer ready", err)
	}
}

func TestTeamHealthRejectsStaleReadinessIdentity(t *testing.T) {
	server, port := newHermesHealthServer(t)
	defer server.Close()

	workspace := t.TempDir()
	readyFile := filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "runtime", "redis-team.ready.json")
	writeStartupState(t, readyFile, map[string]any{
		"ready":      true,
		"state":      "ready",
		"runtime":    "hermes",
		"teamId":     "team-42",
		"memberId":   "developer",
		"instanceId": 63,
		"generation": 6,
	})
	checker := hermes.NewProfile("hermes").HealthChecker(gateway.Config{GatewayStartupTimeout: 180 * time.Millisecond})
	err := checker.WaitReady(context.Background(), teamGatewayStartSpec(workspace, readyFile, port))
	if err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("WaitReady() error = %v, want stale generation rejection", err)
	}
}

func TestTeamHealthReturnsStructuredConsumerFailure(t *testing.T) {
	server, port := newHermesHealthServer(t)
	defer server.Close()

	workspace := t.TempDir()
	readyFile := filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes", "runtime", "redis-team.ready.json")
	writeStartupState(t, readyFile+".failed", map[string]any{
		"ready":      false,
		"state":      "failed",
		"runtime":    "hermes",
		"teamId":     "team-42",
		"memberId":   "developer",
		"instanceId": 63,
		"generation": 7,
		"error": map[string]any{
			"code":      "shared_workspace_unusable",
			"message":   "Team shared directory is unusable",
			"retryable": false,
		},
	})
	checker := hermes.NewProfile("hermes").HealthChecker(gateway.Config{GatewayStartupTimeout: time.Second})
	startedAt := time.Now()
	err := checker.WaitReady(context.Background(), teamGatewayStartSpec(workspace, readyFile, port))
	if err == nil || !strings.Contains(err.Error(), "shared_workspace_unusable") {
		t.Fatalf("WaitReady() error = %v, want structured consumer failure", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("structured failure took %s, want immediate failure", elapsed)
	}
}

func TestNonTeamHealthRemainsHTTPOnly(t *testing.T) {
	server, port := newHermesHealthServer(t)
	defer server.Close()

	checker := hermes.NewProfile("hermes").HealthChecker(gateway.Config{GatewayStartupTimeout: time.Second})
	if err := checker.WaitReady(context.Background(), gateway.GatewayStartSpec{Port: port}); err != nil {
		t.Fatalf("WaitReady() error = %v, want HTTP-only non-Team health", err)
	}
}

func TestTeamHealthRejectsIncompleteConsumerConfiguration(t *testing.T) {
	checker := hermes.NewProfile("hermes").HealthChecker(gateway.Config{GatewayStartupTimeout: time.Second})
	err := checker.WaitReady(context.Background(), gateway.GatewayStartSpec{
		Port: 20000,
		Env:  []string{"CLAWMANAGER_TEAM_ENABLED=true"},
	})
	if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") {
		t.Fatalf("WaitReady() error = %v, want incomplete Team configuration", err)
	}
}

func newHermesHealthServer(t *testing.T) (*httptest.Server, int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	host, rawPort, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil || host == "" {
		server.Close()
		t.Fatalf("parse test server URL %q: %v", server.URL, err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		server.Close()
		t.Fatalf("parse test server port %q: %v", rawPort, err)
	}
	return server, port
}

func teamGatewayStartSpec(workspace, readyFile string, port int) gateway.GatewayStartSpec {
	return gateway.GatewayStartSpec{
		RuntimeType:   "hermes",
		InstanceID:    63,
		WorkspacePath: workspace,
		Port:          port,
		Generation:    7,
		Env: []string{
			"CLAWMANAGER_TEAM_ENABLED=true",
			"CLAWMANAGER_TEAM_REDIS_URL=redis://redis.example.invalid:6379/0",
			"CLAWMANAGER_TEAM_ID=team-42",
			"CLAWMANAGER_TEAM_MEMBER_ID=developer",
			"CLAWMANAGER_TEAM_READY_FILE=" + readyFile,
			"CLAWMANAGER_INSTANCE_ID=63",
			"CLAWMANAGER_GATEWAY_GENERATION=7",
		},
	}
}

func writeStartupState(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func envMap(env []string) map[string]string {
	values := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
