package slots

import (
	"strings"
	"testing"
)

func TestBusinessEnvironmentExcludesManagementCredentials(t *testing.T) {
	result := strings.Join(businessEnvironment([]string{"PATH=/bin", "PATH=/usr/bin", "RUNTIME_AGENT_CONTROL_TOKEN=control", "RUNTIME_AGENT_REPORT_TOKEN=report", "RUNTIME_SLOT_SOCKET=/run/slot.sock", "CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN=bootstrap", "KUBERNETES_SERVICE_HOST=api", "CLAWMANAGER_INSTANCE_TOKEN=business", "CLAWMANAGER_AGENT_PERSISTENT_DIR=/workspaces/a/user-1/instance-2/home/.openclaw"}), "\n")
	for _, secret := range []string{"=control", "=report", "=bootstrap", "=api", "/run/slot.sock", "PATH=/bin"} {
		if strings.Contains(result, secret) {
			t.Fatalf("management state leaked: %s", result)
		}
	}
	for _, required := range []string{"PATH=/usr/bin", "CLAWMANAGER_INSTANCE_TOKEN=business", "CLAWMANAGER_AGENT_PERSISTENT_DIR="} {
		if !strings.Contains(result, required) {
			t.Fatalf("business config lost: %s", result)
		}
	}
}
