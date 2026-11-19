//go:build linux

package hermes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

// An operator-only diagnostic against copies of generated files. No source
// writes, native hooks, network requests or output of file contents/secrets.
func TestOperatorLegacyCredentialCopies(t *testing.T) {
	source := os.Getenv("CM_HERMES_AUDIT_WORKSPACE")
	if source == "" {
		t.Skip("requires explicitly selected legacy workspace")
	}
	type file struct {
		dir, name string
		data      []byte
	}
	var files []file
	for _, dir := range []string{"home/.hermes", "home/.clawmanager-team-worker/.hermes"} {
		entries, err := os.ReadDir(filepath.Join(source, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal("generated directory unreadable")
		}
		for _, e := range entries {
			name := e.Name()
			selected := managedconfig.GeneratedTemporary(name, "")
			for _, base := range []string{"config.yaml", ".env", "gateway.json"} {
				selected = selected || managedconfig.Generated(name, base) || managedconfig.GeneratedTemporary(name, base)
			}
			if !selected {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				t.Fatal("generated file is not regular")
			}
			data, err := os.ReadFile(filepath.Join(source, dir, name))
			if err != nil {
				t.Fatal("generated file unreadable")
			}
			files = append(files, file{dir, name, data})
		}
	}
	run := func(t *testing.T, selected []file) {
		base := t.TempDir()
		workspace := filepath.Join(base, "hermes/user-1/instance-1")
		for _, f := range selected {
			p := filepath.Join(workspace, f.dir, f.name)
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, f.data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg := gateway.Config{RuntimeType: "hermes", WorkspaceRoot: base, NetworkSecurityProfile: "isolated-network-v1"}
		req := gateway.CreateGatewayRequest{InstanceID: 1, UserID: 1, Generation: 1, AgentType: "hermes", WorkspacePath: workspace, SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_diagnostic_not_a_credential", "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL, "CLAWMANAGER_TEAM_REDIS_URL": "redis://clawmanager-local-services.invalid:31283/0", "CLAWMANAGER_TEAM_ID": os.Getenv("CM_HERMES_AUDIT_TEAM")}}
		if err := (Profile{}).MigrateManagedCredentials(context.Background(), cfg, req, workspace); err != nil {
			t.Fatalf("copied generated data rejected: %s", gateway.NativeStorageFailureCode(err))
		}
		if os.Getenv("CM_HERMES_AUDIT_CONFIG") == "1" {
			t.Setenv(desktopWebFlag, "true")
			cfg.PublicOrigin = "https://portal.example.test"
			cfg.TrustedProxies = []string{"10.0.0.1"}
			req.UID, req.GID = os.Geteuid(), os.Getegid()
			if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
				t.Fatalf("copied config writer rejected: %v", err)
			}
		}
	}
	t.Run("combined", func(t *testing.T) { run(t, files) })
	for _, f := range files {
		t.Run(f.dir+"/"+f.name, func(t *testing.T) { run(t, []file{f}) })
	}
}
