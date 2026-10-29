package agent

import "testing"

func TestLoadConfigFromEnvRejectsNonPositiveHeartbeatInterval(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")
	t.Setenv("RUNTIME_AGENT_HEARTBEAT_INTERVAL", "0s")

	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("LoadConfigFromEnv() error = nil, want non-positive heartbeat interval error")
	}
}

func TestLoadConfigFromEnvUsesRuntimeAgentDefaults(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_PUBLIC_ORIGIN", "https://claw.example.com/api/v1/instances/63/proxy")
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "gateway-token")
	t.Setenv("POD_NAME", "openclaw-runtime-abcde")
	t.Setenv("POD_NAMESPACE", "clawmanager-system")
	t.Setenv("POD_IP", "10.42.0.31")
	t.Setenv("NODE_NAME", "node-a")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}

	if cfg.RuntimeType != "openclaw" {
		t.Fatalf("RuntimeType = %q, want openclaw", cfg.RuntimeType)
	}
	if cfg.Runtime == nil || cfg.Runtime.Type() != "openclaw" {
		t.Fatalf("Runtime profile = %#v, want openclaw profile", cfg.Runtime)
	}
	if cfg.Capacity != 100 {
		t.Fatalf("Capacity = %d, want 100", cfg.Capacity)
	}
	if cfg.GatewayPortStart != 20000 || cfg.GatewayPortEnd != 20099 {
		t.Fatalf("gateway port range = %d-%d, want 20000-20099", cfg.GatewayPortStart, cfg.GatewayPortEnd)
	}
	if cfg.GatewayPortBlockSize != 3 {
		t.Fatalf("GatewayPortBlockSize = %d, want 3", cfg.GatewayPortBlockSize)
	}
	if cfg.GatewayAuthMode != "trusted-proxy" {
		t.Fatalf("GatewayAuthMode = %q, want trusted-proxy", cfg.GatewayAuthMode)
	}
	if cfg.AgentEndpoint != "http://10.42.0.31:19090" {
		t.Fatalf("AgentEndpoint = %q, want pod IP endpoint", cfg.AgentEndpoint)
	}
	if cfg.BackendURL != "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001" {
		t.Fatalf("BackendURL = %q, want namespace default", cfg.BackendURL)
	}
	if cfg.ListenAddr != "0.0.0.0:19090" {
		t.Fatalf("ListenAddr = %q, want default listen address", cfg.ListenAddr)
	}
	if cfg.ImageRef != "local/openclaw:dev" {
		t.Fatalf("ImageRef = %q, want env image ref", cfg.ImageRef)
	}
	if cfg.PublicOrigin != "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001" {
		t.Fatalf("PublicOrigin = %q, want K8s ClawManager service origin for OpenClaw trust", cfg.PublicOrigin)
	}
	if got := stringSliceSet(cfg.AllowedOrigins); len(got) != 1 || !got["http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"] {
		t.Fatalf("AllowedOrigins = %#v, want only K8s service origin", cfg.AllowedOrigins)
	}
	if got := stringSliceSet(cfg.TrustedProxies); !got["10.42.0.0/16"] {
		t.Fatalf("TrustedProxies = %#v, want pod-network CIDR inferred from POD_IP", cfg.TrustedProxies)
	}
	if cfg.GatewayToken != "gateway-token" {
		t.Fatalf("GatewayToken = %q, want configured gateway token", cfg.GatewayToken)
	}
	wantCommand := []string{"openclaw", "gateway", "run", "--allow-unconfigured", "--auth", "trusted-proxy", "--bind", "lan", "--force"}
	if !stringSlicesEqual(cfg.GatewayCommand, wantCommand) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, wantCommand)
	}
}

func TestLoadConfigFromEnvNormalizesConfiguredGatewayCommandAuthMode(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "/usr/local/bin/openclaw gateway run --allow-unconfigured --auth token --bind auto --force")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	want := []string{"/usr/local/bin/openclaw", "gateway", "run", "--allow-unconfigured", "--auth", "trusted-proxy", "--bind", "auto", "--force"}
	if !stringSlicesEqual(cfg.GatewayCommand, want) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, want)
	}
}

func TestLoadConfigFromEnvRejectsUnknownRuntimeType(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "other")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")

	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("LoadConfigFromEnv() error = nil, want unknown runtime without command error")
	}
}

func TestLoadConfigFromEnvUsesOpenClawShellProfile(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw-shell")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw-shell:dev")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if cfg.Runtime == nil || cfg.Runtime.Type() != "openclaw-shell" {
		t.Fatalf("Runtime profile = %#v, want openclaw-shell profile", cfg.Runtime)
	}
	wantCommand := []string{"openclaw", "gateway", "run", "--allow-unconfigured", "--auth", "trusted-proxy", "--bind", "auto", "--force"}
	if !stringSlicesEqual(cfg.GatewayCommand, wantCommand) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, wantCommand)
	}
}

func TestLoadConfigFromEnvUsesHermesProfile(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "hermes")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/hermes:dev")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if cfg.Runtime == nil || cfg.Runtime.Type() != "hermes" {
		t.Fatalf("Runtime profile = %#v, want hermes profile", cfg.Runtime)
	}
	if cfg.GatewayPortBlockSize != 1 {
		t.Fatalf("GatewayPortBlockSize = %d, want 1", cfg.GatewayPortBlockSize)
	}
	wantCommand := []string{"start-hermes-desktop"}
	if !stringSlicesEqual(cfg.GatewayCommand, wantCommand) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, wantCommand)
	}
}

func TestLoadConfigFromEnvUsesWindowsVMProfile(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "windows-vm")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/windows-vm:dev")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if cfg.Runtime == nil || cfg.Runtime.Type() != "windows-vm" {
		t.Fatalf("Runtime profile = %#v, want windows-vm profile", cfg.Runtime)
	}
	if cfg.Capacity != 1 {
		t.Fatalf("Capacity = %d, want 1", cfg.Capacity)
	}
	if cfg.GatewayPortStart != 8006 || cfg.GatewayPortEnd != 8006 {
		t.Fatalf("gateway port range = %d-%d, want 8006-8006", cfg.GatewayPortStart, cfg.GatewayPortEnd)
	}
	wantCommand := []string{"/usr/local/bin/windows-vm-gateway"}
	if !stringSlicesEqual(cfg.GatewayCommand, wantCommand) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, wantCommand)
	}
}

func TestLoadConfigFromEnvAllowsUnknownRuntimeWithExplicitCommand(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom-runtime")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/custom:dev")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "/usr/local/bin/custom-runtime serve --port auto")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if cfg.Runtime == nil || cfg.Runtime.Type() != "custom-runtime" {
		t.Fatalf("Runtime profile = %#v, want generic custom-runtime profile", cfg.Runtime)
	}
	wantCommand := []string{"/usr/local/bin/custom-runtime", "serve", "--port", "auto", "--auth", "trusted-proxy"}
	if !stringSlicesEqual(cfg.GatewayCommand, wantCommand) {
		t.Fatalf("GatewayCommand = %#v, want %#v", cfg.GatewayCommand, wantCommand)
	}
}

func TestLoadConfigFromEnvDefaultsToKubernetesClawManagerServiceOrigin(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")
	t.Setenv("POD_NAMESPACE", "clawmanager-system")
	t.Setenv("POD_IP", "10.42.0.31")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	want := "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"
	if cfg.PublicOrigin != want {
		t.Fatalf("PublicOrigin = %q, want K8s ClawManager service origin fallback", cfg.PublicOrigin)
	}
	if got := stringSliceSet(cfg.AllowedOrigins); len(got) != 1 || !got[want] {
		t.Fatalf("AllowedOrigins = %#v, want only K8s service origin", cfg.AllowedOrigins)
	}
}

func TestLoadConfigFromEnvUsesConfiguredTrustedProxies(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control-token")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report-token")
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "local/openclaw:dev")
	t.Setenv("POD_NAMESPACE", "clawmanager-system")
	t.Setenv("POD_IP", "10.42.0.31")
	t.Setenv("OPENCLAW_TRUSTED_PROXIES", "10.42.0.0/16,100.68.0.0/16")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if got := stringSliceSet(cfg.TrustedProxies); len(got) != 2 || !got["10.42.0.0/16"] || !got["100.68.0.0/16"] {
		t.Fatalf("TrustedProxies = %#v, want configured CIDRs", cfg.TrustedProxies)
	}
}

func stringSliceSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
