package hermesimage_test

import (
	"os"
	"strings"
	"testing"
)

func TestProRevocationLauncherIsOptInAndSessionScoped(t *testing.T) {
	raw, err := os.ReadFile("rootfs/usr/local/bin/start-hermes-desktop")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL:-",
		"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE:-",
		"RUNTIME_AGENT_CONTROL_TOKEN:-", "RUNTIME_AGENT_REPORT_TOKEN:-",
		"CLAWMANAGER_TEAM_ENABLED:-", "1|true|yes|on|enabled) ;;",
		"command -v setsid", "exec setsid --wait /bin/bash -c",
		"CLAWMANAGER_HERMES_FAIL_STOP_PGID=\"$$\"",
		"exec hermes desktop --skip-build --cwd /config",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing launcher boundary %q", required)
		}
	}
}
