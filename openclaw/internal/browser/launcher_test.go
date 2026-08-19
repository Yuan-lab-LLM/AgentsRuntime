package browser

import (
	"strings"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

func TestRuntimeWaitURLUsesGenericRouteAndConfiguredTarget(t *testing.T) {
	cfg := appconfig.Config{
		LocalHTTPBind: "0.0.0.0:18080",
		BrowserURL:    "http://127.0.0.1:3080",
	}

	got := runtimeWaitURL(cfg)
	if !strings.HasPrefix(got, "http://127.0.0.1:18080/runtime-wait?") {
		t.Fatalf("runtimeWaitURL() = %q, want generic runtime wait route", got)
	}
	if !strings.Contains(got, "target=http%3A%2F%2F127.0.0.1%3A3080") {
		t.Fatalf("runtimeWaitURL() = %q, want encoded DeepSeek target", got)
	}
	if strings.Contains(got, "openclaw-wait") {
		t.Fatalf("runtimeWaitURL() = %q, should not expose OpenClaw branding", got)
	}
}
