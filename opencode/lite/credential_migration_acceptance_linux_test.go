//go:build linux

package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeCredentialMigrationAcceptance(t *testing.T) {
	cfg, req, old, custom := nativeAcceptanceSetup(t, "opencode")
	workspace := req.WorkspacePath
	raw := fmt.Sprintf(`{"provider":{"clawmanager":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:9/api/v1/gateway/llm","apiKey":%q},"models":{"auto":{"name":"Auto"}}},"personal":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://third-party.invalid/v1","apiKey":%q},"models":{"custom":{"name":"Preserved custom model"}}}}}`, old, custom)
	for _, name := range []string{"opencode.json", "opencode.json.bak", ".cm-0123456789abcdef01234567.tmp"} {
		nativeAcceptanceWrite(t, workspace, "home/.opencode/"+name, []byte(raw))
	}
	spec := nativeAcceptanceLifecycle(t, cfg, req, NewProfile("opencode"), old)
	nativeAcceptanceScan(t, workspace, old)
	nativeAcceptanceContains(t, filepath.Join(workspace, "home/.opencode/opencode.json"), custom)
	env := append(append([]string{}, spec.Env...), "OPENCODE_DISABLE_MODELS_FETCH=true", "OPENCODE_DISABLE_AUTOUPDATE=true", "OPENCODE_DISABLE_PLUGINS=true")
	output := nativeAcceptanceCommand(t, workspace, env, "/usr/local/bin/opencode", "debug", "config")
	if bytes.Contains(output, []byte(old)) || !bytes.Contains(output, []byte(managedconfig.ModelURL)) || !bytes.Contains(output, []byte(custom)) {
		t.Fatal("installed OpenCode config loader changed managed/custom configuration")
	}
	nativeAcceptanceScan(t, workspace, old)
	t.Log("installed OpenCode debug config loaded actual generated config offline; no gateway or inference was executed")
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
