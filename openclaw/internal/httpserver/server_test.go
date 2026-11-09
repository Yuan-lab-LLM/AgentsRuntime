package httpserver

import (
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
)

func TestRuntimeWaitReadyDoesNotRequireGatewayWarmup(t *testing.T) {
	if !runtimeWaitReady(process.Snapshot{Status: process.StatusRunning, GatewayWarmupReady: false}) {
		t.Fatal("running gateway should release wait page even while models warmup continues")
	}
}

func TestRuntimeWaitReadyDoesNotReleaseStartingGateway(t *testing.T) {
	if runtimeWaitReady(process.Snapshot{Status: process.StatusStarting}) {
		t.Fatal("starting gateway should not release wait page before gateway readiness promotion")
	}
}

func TestRuntimeWaitReadyReleasesWhenWarmupStarted(t *testing.T) {
	if !runtimeWaitReady(process.Snapshot{Status: process.StatusStarting, GatewayWarmupStarted: true}) {
		t.Fatal("started warmup should release wait page into bounded warmup wait")
	}
}

func TestRuntimeWaitPageStartsWarmupTimeoutAfterGatewayReady(t *testing.T) {
	page := runtimeWaitPage("http://localhost:18789", "openclaw", "OpenClaw")
	if !strings.Contains(page, "let gatewayReadyAt = 0") {
		t.Fatal("wait page should track when the gateway first becomes ready")
	}
	if !strings.Contains(page, "gatewayReadyAt = Date.now()") {
		t.Fatal("wait page should start warmup timeout after gateway readiness")
	}
	if strings.Contains(page, "const startedAt = Date.now()") {
		t.Fatal("wait page should not start warmup timeout at page load")
	}
}

func TestRuntimeWaitPageUsesServerSideGatewayTokenForOpenClaw(t *testing.T) {
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "token /?+&=")

	page := runtimeWaitPage("https://untrusted.example.invalid/", "openclaw", "OpenClaw")
	wantTarget := `const target = "http://localhost:18789/#token=token+%2F%3F%2B%26%3D";`
	if !strings.Contains(page, wantTarget) {
		t.Fatal("wait page target does not contain URL-encoded server token")
	}
	if strings.Contains(page, "untrusted.example.invalid") {
		t.Fatal("wait page must not attach the gateway token to a caller-provided target")
	}
	if strings.Contains(page, "/openclaw-wait?target=") {
		t.Fatal("wait page must not put the gateway token in a target query parameter")
	}
}

func TestRuntimeWaitPageWithoutGatewayTokenKeepsOriginalTarget(t *testing.T) {
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "")

	page := runtimeWaitPage("http://localhost:18789/workspace", "openclaw", "OpenClaw")
	if !strings.Contains(page, `const target = "http://localhost:18789/workspace";`) {
		t.Fatal("wait page should keep the original target when no gateway token is configured")
	}
	if strings.Contains(page, "#token=") {
		t.Fatal("wait page should not add a token fragment when no gateway token is configured")
	}
}

func TestRuntimeWaitPageKeepsDeepSeekTargetAndBranding(t *testing.T) {
	t.Setenv("OPENCLAW_GATEWAY_TOKEN", "openclaw-secret")

	page := runtimeWaitPage("http://127.0.0.1:3080", "deepseek-harness", "DeepSeek Harness Pro")
	if !strings.Contains(page, `const target = "http://127.0.0.1:3080";`) {
		t.Fatal("DeepSeek wait page should keep the configured runtime target")
	}
	if strings.Contains(page, "#token=") || strings.Contains(page, "localhost:18789") {
		t.Fatal("DeepSeek wait page should not use the OpenClaw gateway target or token")
	}
	if !strings.Contains(page, "<title>DeepSeek Harness Pro</title>") {
		t.Fatal("DeepSeek wait page should use the configured runtime name")
	}
	if strings.Contains(page, "<title>OpenClaw</title>") || strings.Contains(page, "&#40857;&#34430;") {
		t.Fatal("DeepSeek wait page should not contain OpenClaw branding")
	}
	if !strings.Contains(page, "const runtimeRequiresWarmup = false;") {
		t.Fatal("DeepSeek wait page should redirect as soon as the runtime is ready")
	}
}
