package agent

import (
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime"
)

func TestPoolConfigRequiresTLSAndAdvertisesHTTPSIPv6(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "custom")
	t.Setenv("RUNTIME_GATEWAY_COMMAND", "custom-serve")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report")
	t.Setenv("RUNTIME_AGENT_ROLE", "pool")
	t.Setenv("RUNTIME_AGENT_LISTEN_ADDR", "")
	t.Setenv("POD_IP", "fd00::2")
	t.Setenv("RUNTIME_AGENT_TLS_CERT_FILE", "")
	t.Setenv("RUNTIME_AGENT_TLS_KEY_FILE", "")
	profile := runtime.StaticProfile{RuntimeType: "custom", RuntimeDefaults: gateway.RuntimeDefaults{WorkspaceRoot: t.TempDir(), AgentDataDir: t.TempDir(), GatewayPortStart: 23000, GatewayPortEnd: 23002, GatewayPortBlockSize: 1, GatewayCapacity: 3, GatewayAuthMode: "token"}}
	if _, err := LoadConfigFromEnv(profile); err == nil {
		t.Fatal("pool accepted plaintext management")
	}
	t.Setenv("RUNTIME_AGENT_TLS_CERT_FILE", "/var/run/clawmanager-agent-tls/tls.crt")
	t.Setenv("RUNTIME_AGENT_TLS_KEY_FILE", "/var/run/clawmanager-agent-tls/tls.key")
	cfg, err := LoadConfigFromEnv(profile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentEndpoint != "https://[fd00::2]:19090" || cfg.ListenAddr != ":19090" || cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		t.Fatalf("pool TLS endpoint missing: %+v", cfg)
	}
	t.Setenv("RUNTIME_AGENT_ROLE", "")
	t.Setenv("RUNTIME_AGENT_TLS_CERT_FILE", "")
	t.Setenv("RUNTIME_AGENT_TLS_KEY_FILE", "")
	cfg, err = LoadConfigFromEnv(profile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentEndpoint != "http://[fd00::2]:19090" {
		t.Fatal("legacy scheme changed")
	}
}
