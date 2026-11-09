package agent

import "testing"

func TestDeploymentNameUsesExplicitPoolIdentityBeforeLegacyInference(t *testing.T) {
	const target = "openclaw-runtime-u113-u123-u125-u127-u130-u132-u134"
	for _, tc := range []struct{ name, managed, legacy, generic, pod, want string }{
		{"long Kubernetes pod", target, "", "", target + "-6f8995dn4cs", target},
		{"managed overrides inherited alias", target, "old-pool", "other-pool", "pod", target},
		{"legacy alias", "", "legacy-pool", "generic", "pod", "legacy-pool"},
		{"generic alias", "", "", "generic-pool", "pod", "generic-pool"},
		{"old unnamed deployment", "", "", "", "openclaw-runtime-abcde-12345", "openclaw-runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAWMANAGER_RUNTIME_DEPLOYMENT_NAME", tc.managed)
			t.Setenv("CLAWMANAGER_DEPLOYMENT_NAME", tc.legacy)
			t.Setenv("DEPLOYMENT_NAME", tc.generic)
			if got := deploymentName(tc.pod, "openclaw"); got != tc.want {
				t.Fatalf("deployment=%q want=%q", got, tc.want)
			}
		})
	}
}
