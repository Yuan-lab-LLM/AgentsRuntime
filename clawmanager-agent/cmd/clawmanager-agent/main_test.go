package main

import "testing"

func TestSelectModeUsesInstanceAgentForOpenCode(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "desktop")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "opencode")

	if got := selectMode(); got != modeInstance {
		t.Fatalf("selectMode() = %s, want %s", got, modeInstance)
	}
}

func TestSelectModePrefersRuntimePodAgent(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "hermes")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "")

	if got := selectMode(); got != modeRuntimePod {
		t.Fatalf("selectMode() = %s, want %s", got, modeRuntimePod)
	}
}

func TestSelectModeUsesInstanceAgentWhenEnabled(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "hermes")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "")

	if got := selectMode(); got != modeInstance {
		t.Fatalf("selectMode() = %s, want %s", got, modeInstance)
	}
}

func TestSelectModeDisabledWithoutRuntimeTokens(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "")

	if got := selectMode(); got != modeDisabled {
		t.Fatalf("selectMode() = %s, want %s", got, modeDisabled)
	}
}

func TestSelectModeDoesNotRunInstanceAgentForOtherRuntime(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "openclaw")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "openclaw")

	if got := selectMode(); got != modeDisabled {
		t.Fatalf("selectMode() = %s, want %s", got, modeDisabled)
	}
}

func TestSelectModeUsesAgentRuntimeTypeOverDesktopBackend(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "desktop")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "hermes")

	if got := selectMode(); got != modeInstance {
		t.Fatalf("selectMode() = %s, want %s", got, modeInstance)
	}
}

func TestSelectModeDesktopBackendWithoutAgentRuntimeType(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "desktop")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "")

	if got := selectMode(); got != modeInstance {
		t.Fatalf("selectMode() = %s, want %s", got, modeInstance)
	}
}

func TestSelectModePrefersInstanceWhenLiteTokensAlsoPresent(t *testing.T) {
	t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", "control")
	t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", "report")
	t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
	t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "4")
	t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
	t.Setenv("CLAWMANAGER_RUNTIME_TYPE", "desktop")
	t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", "hermes")

	if got := selectMode(); got != modeInstance {
		t.Fatalf("selectMode() = %s, want %s", got, modeInstance)
	}
}
