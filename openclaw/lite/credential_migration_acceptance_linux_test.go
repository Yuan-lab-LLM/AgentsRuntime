//go:build linux

package openclaw

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeCredentialMigrationAcceptance(t *testing.T) {
	cfg, req, old, custom := nativeAcceptanceSetup(t, "openclaw")
	cfg.GatewayPortBlockSize = 3
	// Production image provides this nonsecret version metadata. The acceptance
	// runner's env -i must not accidentally select pre-8.1 writer defaults.
	packageData, err := os.ReadFile("/usr/local/lib/node_modules/openclaw/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var installed struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal(packageData, &installed); err != nil || installed.Version != "2026.10.2" {
		t.Fatal("installed OpenClaw package is not the pinned acceptance version")
	}
	cfg.OpenClawVersion = installed.Version
	workspace := req.WorkspacePath
	provider := fmt.Sprintf(`{"baseUrl":"http://127.0.0.1:9/api/v1/gateway/llm","api":"openai-completions","apiKey":%q,"models":[{"id":"auto","name":"Auto","contextWindow":16000,"maxTokens":1024}]}`, old)
	raw := fmt.Sprintf(`{"models":{"mode":"merge","providers":{"auto":%s,"personal":{"baseUrl":"https://third-party.invalid/v1","api":"openai-completions","apiKey":%q,"models":[{"id":"personal","name":"Personal","contextWindow":16000,"maxTokens":1024}]}}},"gateway":{"auth":{"mode":"token","token":%q}}}`, provider, custom, old)
	nativeAcceptanceWrite(t, workspace, "home/.openclaw/openclaw.json", []byte(raw))
	// Historical generated backups may contain the prior Team account even
	// when the current target is standalone. Use its actual plugin schema, not
	// unsupported top-level env assignments invented by the initial fixture.
	backup := strings.TrimSuffix(raw, "}") + fmt.Sprintf(`,"channels":{"redis-team":{"accounts":{"default":{"redisUrl":"redis://old:%s@127.0.0.1:9/4","teamToken":%q}}}}}`, old, old)
	for _, name := range []string{"openclaw.json.bak", "openclaw.json.bak-browser-worker"} {
		nativeAcceptanceWrite(t, workspace, "home/.openclaw/"+name, []byte(backup))
	}
	state := fmt.Sprintf(`{"urls":["http://old:%s@clawbrowser-42.acceptance.svc:9222"],"custom":%q}`, old, custom)
	nativeAcceptanceWrite(t, workspace, "home/.openclaw/openclaw.json.browser-worker-state", []byte(state))
	nativeAcceptanceWrite(t, workspace, "home/.openclaw/agents/main/agent/.acceptance-owned", []byte("fixture"))
	beforeEnv := append(nativeAcceptanceEnv(workspace), "OPENCLAW_STATE_DIR="+filepath.Join(workspace, "home/.openclaw"), "OPENCLAW_CONFIG_PATH="+filepath.Join(workspace, "home/.openclaw/openclaw.json"))
	// This is the actual installed 10.2 writer, not a Go models.json substitute.
	nativeAcceptanceOpenClawModels(t, workspace, beforeEnv)
	models := filepath.Join(workspace, "home/.openclaw/agents/main/agent/models.json")
	nativeAcceptanceContains(t, models, old)
	spec := nativeAcceptanceLifecycle(t, cfg, req, NewProfile("openclaw"), old)
	nativeAcceptanceScan(t, workspace, old)
	nativeAcceptanceContains(t, filepath.Join(workspace, "home/.openclaw/openclaw.json"), custom)
	nativeAcceptanceContains(t, models, custom)
	nativeAcceptanceOpenClawModels(t, workspace, spec.Env)
	nativeAcceptanceScan(t, workspace, old)
	nativeAcceptanceContains(t, models, managedconfig.ModelURL)
	nativeAcceptanceContains(t, models, custom)
	nativeAcceptanceCommand(t, workspace, spec.Env, "/usr/local/bin/openclaw", "config", "validate", "--json")
	nativeAcceptanceScan(t, workspace, old)
	t.Log("installed OpenClaw models writer before/after and CLI config validation passed offline; no gateway or inference was executed")
}
func nativeAcceptanceOpenClawModels(t *testing.T, workspace string, env []string) {
	const script = `import fs from 'node:fs/promises';import path from 'node:path';
const {t:ensure}=await import('/usr/local/lib/node_modules/openclaw/dist/models-config-ClfA-HhA.mjs');
const config=JSON.parse(await fs.readFile(process.env.OPENCLAW_CONFIG_PATH,'utf8'));
await ensure(config,path.join(process.env.OPENCLAW_STATE_DIR,'agents/main/agent'),{env:process.env,providerDiscoveryProviderIds:[]});
console.log('installed_models_writer_completed');`
	nativeAcceptanceCommand(t, workspace, env, "/usr/local/bin/node", "--input-type=module", "-e", script)
}

// The starter captures the actual lifecycle's final launch specification. It
// deliberately does NOT execute a gateway or claim network isolation success.
// Native compatibility below is established only by separate installed loaders.
type nativeAcceptanceCapture struct{ spec chan gateway.GatewayStartSpec }

func (c nativeAcceptanceCapture) PrepareGateway(context.Context, gateway.CreateGatewayRequest, int) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}
func (c nativeAcceptanceCapture) StartGateway(_ context.Context, spec gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	c.spec <- spec
	return gateway.ManagedProcess{}, errors.New("acceptance_capture_stop_before_gateway_execution")
}

const nativeAcceptanceUID = 22042
const nativeAcceptancePath = "/opt/hermes-agent/.venv/bin:/usr/local/bin:/usr/bin:/bin"

func nativeAcceptanceSetup(t *testing.T, runtime string) (gateway.Config, gateway.CreateGatewayRequest, string, string) {
	t.Helper()
	if os.Getenv("CLAWMANAGER_NATIVE_ACCEPTANCE") != "1" {
		t.Skip("requires explicit Linux native-image acceptance gate")
	}
	if os.Geteuid() != 0 {
		t.Fatal("acceptance requires root for real instance UID ownership and offline net namespace")
	}
	root, err := os.MkdirTemp("/tmp", "cm-native-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove temporary acceptance workspace: %v", err)
		}
	})
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, runtime, "user-1", "instance-42")
	if err = os.MkdirAll(filepath.Dir(workspace), 0755); err != nil {
		t.Fatal(err)
	}
	nativeAcceptanceWrite(t, workspace, "home/acceptance-owned.txt", []byte("temporary acceptance fixture"))
	cfg := gateway.Config{RuntimeType: runtime, WorkspaceRoot: root, IsolatedLifecycle: true, NetworkSecurityProfile: "isolated-network-v1", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 1, Capacity: 1, GatewayAuthMode: "token", PublicOrigin: "http://cm-acceptance.invalid", TrustedProxies: []string{"127.0.0.1"}, GatewayBaseEnv: []string{"PATH=" + nativeAcceptancePath}}
	req := gateway.CreateGatewayRequest{AgentType: runtime, InstanceID: 42, UserID: 1, UID: nativeAcceptanceUID, GID: nativeAcceptanceUID, Generation: 1, WorkspacePath: workspace, SecurityProfile: cfg.NetworkSecurityProfile, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_acceptance_" + rand.Text(), "CLAWMANAGER_LLM_API_KEY": managedconfig.ModelKey, "CLAWMANAGER_LLM_BASE_URL": managedconfig.ModelURL, "CLAWMANAGER_LLM_MODEL": "auto", "CLAWMANAGER_TEAM_ID": "acceptance-team", "CLAWMANAGER_TEAM_MEMBER_ID": "acceptance-member", "CLAWMANAGER_TEAM_TOKEN": managedconfig.TeamKey, "CLAWMANAGER_TEAM_REDIS_URL": "redis://clawmanager-local-services.invalid:31283/4", "CLAWMANAGER_MANAGED_SERVICES_URL": managedconfig.ServicesURL, "CLAWMANAGER_NETWORK_ISOLATED": "1"}}
	return cfg, req, "old-cm-fake-" + rand.Text(), "third-party-fake-" + rand.Text()
}
func nativeAcceptanceWrite(t *testing.T, workspace, relative string, data []byte) {
	t.Helper()
	target := filepath.Join(workspace, relative)
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		t.Fatal(err)
	}
	// This is our fresh fixture only. Production migration must never recurse
	// through arbitrary existing user files to assign their ownership.
	for dir := filepath.Dir(target); ; dir = filepath.Dir(dir) {
		if err := os.Chown(dir, nativeAcceptanceUID, nativeAcceptanceUID); err != nil {
			t.Fatal(err)
		}
		if dir == workspace {
			break
		}
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, nativeAcceptanceUID, nativeAcceptanceUID); err != nil {
		t.Fatal(err)
	}
}
func nativeAcceptanceLifecycle(t *testing.T, cfg gateway.Config, req gateway.CreateGatewayRequest, profile gateway.RuntimeProfile, old string) gateway.GatewayStartSpec {
	t.Helper()
	cfg.Runtime = profile
	captured := make(chan gateway.GatewayStartSpec, 1)
	manager := gateway.NewGatewayManager(cfg, nativeAcceptanceCapture{captured}, gateway.NewPortAllocator(func(int) bool { return false }))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("join acceptance lifecycle: %v", err)
		}
	})
	result, err := manager.CreateGateway(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case spec := <-captured:
			if strings.Contains(strings.Join(append(append([]string{}, spec.Env...), spec.Command...), "\n"), old) {
				t.Fatal("old CM sentinel survived final launch env/argv")
			}
			if spec.UID != nativeAcceptanceUID || spec.WorkspacePath != req.WorkspacePath {
				t.Fatal("native lifecycle identity changed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := manager.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			t.Log("real protected GatewayManager migration/prepare/config/env reached captured launch boundary; gateway execution intentionally refused")
			return spec
		case <-tick.C:
			if state, ok := manager.GatewayState(result.GatewayID); ok && state.State == "error" && len(captured) == 0 {
				t.Fatalf("real native lifecycle failed before capture: %s", state.ErrorMessage)
			}
		case <-deadline.C:
			t.Fatal("real native lifecycle did not reach capture")
		}
	}
}
func nativeAcceptanceScan(t *testing.T, workspace, old string) {
	t.Helper()
	files, total := 0, int64(0)
	err := filepath.WalkDir(filepath.Join(workspace, "home"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unexpected nonregular acceptance output")
		}
		files++
		total += info.Size()
		if files > 2048 || info.Size() > 8<<20 || total > 64<<20 {
			return errors.New("acceptance scan bound exceeded")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(old)) {
			return fmt.Errorf("old CM sentinel remains in %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func nativeAcceptanceContains(t *testing.T, path, value string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(value)) {
		t.Fatalf("expected preserved/configured value missing in %s", filepath.Base(path))
	}
}

// All installed native code runs with no inherited credentials, as the real
// nonroot instance UID, inside a fresh offline network namespace. Missing
// unshare/capabilities are acceptance failures, never a networked fallback.
func nativeAcceptanceCommand(t *testing.T, workspace string, env []string, command ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	args := append([]string{"--net", "--setgid", strconv.Itoa(nativeAcceptanceUID), "--setuid", strconv.Itoa(nativeAcceptanceUID), "--"}, command...)
	cmd := exec.CommandContext(ctx, "/usr/bin/unshare", args...)
	cmd.Dir = workspace
	cmd.Env = append([]string{}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output nativeAcceptanceOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if len(command) >= 3 && filepath.Base(command[0]) == "openclaw" && command[1] == "config" && command[2] == "validate" {
			nativeAcceptanceValidationDiagnostics(t, output.Bytes(), env)
		}
		t.Fatalf("installed native acceptance command %s failed: %v (output deliberately suppressed; %d bytes)", filepath.Base(command[0]), err, output.Len())
	}
	return output.Bytes()
}

type nativeAcceptanceOutput struct{ bytes.Buffer }

func (b *nativeAcceptanceOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("native acceptance output bound exceeded")
	}
	return b.Buffer.Write(p)
}
func nativeAcceptanceEnv(workspace string) []string {
	return []string{"PATH=" + nativeAcceptancePath, "HOME=" + filepath.Join(workspace, "home"), "XDG_CONFIG_HOME=" + filepath.Join(workspace, "home/.config"), "XDG_CACHE_HOME=" + filepath.Join(workspace, "home/.cache"), "XDG_DATA_HOME=" + filepath.Join(workspace, "home/.local/share")}
}

// Fake-only validation diagnostics expose schema paths/messages, never the
// complete native document, environment, argv, or raw subprocess output.
func nativeAcceptanceValidationDiagnostics(t *testing.T, data []byte, env []string) {
	t.Helper()
	if len(data) > 8192 {
		t.Log("validation diagnostics omitted: size limit")
		return
	}
	redact := func(value string) string {
		for _, assignment := range env {
			_, secret, ok := strings.Cut(assignment, "=")
			if ok && len(secret) >= 8 {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
		value = regexp.MustCompile(`(?:old-cm-fake-|third-party-fake-|ui1_acceptance_)[A-Za-z0-9_-]+`).ReplaceAllString(value, "[fake-redacted]")
		value = regexp.MustCompile(`(?:https?|redis|wss?)://[^ \n\t"']+`).ReplaceAllString(value, "[url-redacted]")
		if len(value) > 400 {
			value = value[:400] + "[truncated]"
		}
		return value
	}
	count := 0
	var visit func(any)
	visit = func(value any) {
		if count >= 12 {
			return
		}
		switch value := value.(type) {
		case map[string]any:
			for _, key := range []string{"path", "message", "code"} {
				if text, ok := value[key].(string); ok {
					t.Logf("native validation %s: %s", key, redact(text))
					count++
				}
			}
			for _, key := range []string{"issues", "errors", "warnings"} {
				if child, ok := value[key]; ok {
					visit(child)
				}
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	for i, b := range data {
		if b == '{' {
			var value any
			if json.Unmarshal(data[i:], &value) == nil {
				visit(value)
				return
			}
		}
	}
	t.Log("validation output was not a standalone JSON diagnostic; raw content suppressed")
}
