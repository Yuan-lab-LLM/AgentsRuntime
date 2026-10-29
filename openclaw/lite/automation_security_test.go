package openclaw

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/scheduledtasks"
)

func TestIsolatedAutomationsNeverUseAmbientLegacyRunner(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ambient-command-ran")
	if err := os.WriteFile(filepath.Join(dir, "openclaw"), []byte("#!/bin/sh\nprintf '%s' \"$UNRECOGNIZED_SUPERVISOR_SECRET\" > \"$AUTOMATION_TEST_MARKER\"\nprintf '{\"jobs\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUTOMATION_TEST_MARKER", marker)
	t.Setenv("UNRECOGNIZED_SUPERVISOR_SECRET", "management-secret")
	cfg := gateway.Config{RuntimeType: "openclaw", IsolatedLifecycle: true, OpenClawVersion: OpenClaw81Version, GatewayAuthMode: "token", GatewayToken: "business-token"}
	req := gateway.CreateGatewayRequest{Environment: map[string]string{scheduledtasks.EnvOpenClaw: `{"schemaVersion":1,"items":[]}`}}
	hook := NewProfile("openclaw").GatewayAutomations(cfg, req)
	if hook == nil {
		t.Fatal("test requires configured automation hook")
	}
	err := hook(context.Background(), 20000)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("isolated automation executed ambient supervisor command")
	}
	if err == nil {
		t.Fatal("missing managed runner did not fail closed")
	}
}

func TestManagedAutomationsUseOwnStateAndPreserveStopFenceErrors(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "openclaw", "user-2", "instance-7")
	cfg := gateway.Config{RuntimeType: "openclaw", WorkspaceRoot: root, IsolatedLifecycle: true, OpenClawVersion: OpenClaw81Version, GatewayAuthMode: "trusted-proxy"}
	req := gateway.CreateGatewayRequest{UserID: 2, InstanceID: 7, WorkspacePath: workspace, Environment: map[string]string{scheduledtasks.EnvOpenClaw: `{"schemaVersion":1,"items":[]}`, "CLAWMANAGER_INSTANCE_TOKEN": "business-password", "HOME": "/outside", "OPENCLAW_STATE_DIR": "/outside"}}
	calls := 0
	execute := func(ctx context.Context, args, env []string) ([]byte, error) {
		calls++
		values := map[string]string{}
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			values[key] = value
		}
		if len(args) < 3 || args[0] != "openclaw" || args[1] != "automations" || values["HOME"] != filepath.Join(workspace, "home") || values["OPENCLAW_STATE_DIR"] != filepath.Join(workspace, "home", ".openclaw") || values["OPENCLAW_CONFIG_PATH"] != filepath.Join(workspace, "home", ".openclaw", "openclaw.json") || values["OPENCLAW_GATEWAY_PASSWORD"] != "business-password" || values["OPENCLAW_GATEWAY_TOKEN"] != "" {
			t.Fatalf("wrong managed automation command: args=%v env=%v", args, values)
		}
		return nil, fmt.Errorf("namespace still alive: %w", gateway.ErrGatewayStopFailed)
	}
	hook := NewProfile("openclaw").ManagedGatewayAutomations(cfg, req, execute)
	if err := hook(context.Background(), 20000); !errors.Is(err, gateway.ErrGatewayStopFailed) || calls != 1 {
		t.Fatalf("stop-fence error lost: %v calls=%d", err, calls)
	}
}
