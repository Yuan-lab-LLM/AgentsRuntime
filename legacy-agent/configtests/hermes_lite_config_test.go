package agent

import (
	"testing"
)

func TestHermesLiteRequiresInternalBackendAndAcceptsTaggedImage(t *testing.T) {
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "hermes")
	t.Setenv("CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED", "true")
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report")
	t.Setenv("CLAWMANAGER_BACKEND_URL", "http://clawmanager-gateway.platform.svc:9001")
	t.Setenv("CLAWMANAGER_CONTROL_UI_ORIGIN", "")
	t.Setenv("CLAWMANAGER_TRUSTED_PROXY_CIDRS", "")
	for _, image := range []string{"example/hermes:latest", "example/hermes:v2026.9.12", "registry.local/hermes:offline"} {
		t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", image)
		cfg, err := LoadConfigFromEnv()
		if err != nil {
			t.Errorf("LoadConfigFromEnv() rejected tagged image %q: %v", image, err)
			continue
		}
		if cfg.ImageRef != image {
			t.Errorf("ImageRef = %q, want %q", cfg.ImageRef, image)
		}
		if cfg.PublicOrigin != "" || len(cfg.TrustedProxies) != 0 {
			t.Error("inferred proxy trust in Lite mode")
		}
	}
	t.Setenv("CLAWMANAGER_RUNTIME_IMAGE_REF", "example/hermes:latest")
	t.Setenv("CLAWMANAGER_BACKEND_URL", "https://public.example")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("external backend accepted")
	}
}
