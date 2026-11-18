package opencode

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestOpenCodeNativeUpgradeProvider(t *testing.T) {
	p := NewProfile("opencode")
	if _, ok := any(p).(gateway.RuntimeUpgradeProvider); !ok {
		t.Fatal("OpenCode lacks the native preserve adapter")
	}
	if _, ok := any(p).(gateway.ScopedNativeUpgradeProvider); !ok {
		t.Fatal("OpenCode lacks the scoped native preserve adapter")
	}
}

func TestOpenCodeNativeBaselineMatchesObservedImage(t *testing.T) {
	if OpenCodeNativeVersion != "1.18.13" || OpenCodeNativeRevision != "a105350812f05f914c768e468559dbd6bd508d8e" {
		t.Fatal("preservation baseline differs from the observed deployed native binary")
	}
}

func TestOpenCodeNativeOverlayDeclaresRuntimeIdentity(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile.lite-agent-overlay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `io.clawmanager.runtime.type="opencode"`) {
		t.Fatal("native overlay is missing the CM runtime type contract")
	}
}

func TestOpenCodePreserveCapabilityIsDeliveredImageOnly(t *testing.T) {
	p := NewProfile("opencode")
	t.Setenv("CLAWMANAGER_OPENCODE_UPGRADE_PROTOCOL", "")
	if metadata := p.RegistrationMetadata(gateway.Config{}); metadata.ProtocolVersion != "" || len(metadata.Capabilities) != 0 {
		t.Fatal("legacy image advertised unsupported native preservation")
	}
	t.Setenv("CLAWMANAGER_OPENCODE_UPGRADE_PROTOCOL", "opencode-preserve-v1")
	metadata := p.RegistrationMetadata(gateway.Config{})
	if metadata.ProtocolVersion != "opencode-preserve-v1" || metadata.SessionStore != "sqlite" || len(metadata.Capabilities) != 4 || metadata.OpenClawVersion != "" {
		t.Fatalf("missing native delivery metadata: %+v", metadata)
	}
}

func TestOpenCodeDirectUpgradeCannotBypassScopedMaintenance(t *testing.T) {
	service := NewProfile("opencode").UpgradeService(gateway.Config{}, nil)
	if _, err := service.MigrateSessionSQLite(context.Background(), gateway.WorkspaceUpgradeRequest{}); err == nil {
		t.Fatal("direct call bypassed container and stopped writer proof")
	}
}

func TestOpenCodeRejectsDatabaseAndChannelOverrides(t *testing.T) {
	for _, name := range []string{"OPENCODE_DB", "OPENCODE_CHANNEL", "OPENCODE_DISABLE_CHANNEL_DB"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "unreviewed")
			if err := validateOpenCodeNativePayload(context.Background()); err == nil || !strings.Contains(err.Error(), "overrides") {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenCodeNativeSQLitePreservation(t *testing.T) {
	cmd := exec.Command("/usr/bin/python3", "-I", "-B", "upgrade_helper_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native SQLite regression: %v\n%s", err, output)
	}
}
