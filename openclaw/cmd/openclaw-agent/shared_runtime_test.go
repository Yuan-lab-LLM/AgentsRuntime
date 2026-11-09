package main

import "testing"

func TestSharedRuntimeDoesNotStartDesktopGateway(t *testing.T) {
	for _, tc := range []struct {
		name, control, report string
		shared                bool
	}{
		{"standalone", "", "", false},
		{"Pro instance", "", "", false},
		{"shared pool", "control", "report", true},
		{"shared control only", "control", "", true},
		{"shared report only", "", "report", true},
		{"blank", " ", " ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNTIME_AGENT_CONTROL_TOKEN", tc.control)
			t.Setenv("RUNTIME_AGENT_REPORT_TOKEN", tc.report)
			t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
			if got := sharedRuntimeAgentEnabled(); got != tc.shared {
				t.Fatalf("shared=%v want=%v", got, tc.shared)
			}
		})
	}
}
