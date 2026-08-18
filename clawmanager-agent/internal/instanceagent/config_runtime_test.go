package instanceagent

import (
	"path/filepath"
	"testing"
)

func TestLoadConfigUsesManagedRuntimeIdentity(t *testing.T) {
	for _, testCase := range []struct {
		runtimeType string
		command     string
	}{
		{runtimeType: "codex", command: "codex"},
		{runtimeType: "claude-code", command: "claude"},
	} {
		t.Run(testCase.runtimeType, func(t *testing.T) {
			t.Setenv("CLAWMANAGER_AGENT_ENABLED", "true")
			t.Setenv("CLAWMANAGER_AGENT_BASE_URL", "http://gateway")
			t.Setenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN", "boot")
			t.Setenv("CLAWMANAGER_AGENT_INSTANCE_ID", "9")
			persistentDir := t.TempDir()
			t.Setenv("CLAWMANAGER_AGENT_PERSISTENT_DIR", persistentDir)
			t.Setenv("CLAWMANAGER_AGENT_RUNTIME_TYPE", testCase.runtimeType)

			cfg, err := LoadConfig("test")
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.AgentID != testCase.runtimeType+"-9-main" || cfg.RuntimeCommand != testCase.command || cfg.WorkDir() != filepath.Join(persistentDir, testCase.runtimeType+"-agent") {
				t.Fatalf("config = %+v, workdir = %q", cfg, cfg.WorkDir())
			}
		})
	}
}
