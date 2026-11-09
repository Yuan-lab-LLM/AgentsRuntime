package supervisor

import "testing"

func TestOpenClaw81Capabilities(t *testing.T) {
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.8.1")
	got := map[string]bool{}
	for _, capability := range defaultCapabilities() {
		got[capability] = true
	}
	for _, capability := range []string{
		"openclaw.state.sqlite",
		"openclaw.automation.rpc",
		"openclaw.backup.sqlite",
		"openclaw.database.preflight",
		"openclaw.plugin.capability-consent",
	} {
		if !got[capability] {
			t.Fatalf("missing OpenClaw 8.1 capability %q", capability)
		}
	}
}

func TestOpenClaw71DoesNotAdvertise81Capabilities(t *testing.T) {
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.7.1-2")
	for _, capability := range defaultCapabilities() {
		if capability == "openclaw.state.sqlite" {
			t.Fatal("7.1 runtime must not advertise 8.1 SQLite state")
		}
	}
}
