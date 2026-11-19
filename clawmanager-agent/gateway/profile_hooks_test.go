package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type injectedPolicyProfile struct{ runtimeNeutralProfile }

func (injectedPolicyProfile) RegistrationMetadata(Config) RuntimeRegistrationMetadata {
	return RuntimeRegistrationMetadata{ProtocolVersion: "custom-v1", SessionStore: "custom"}
}
func (injectedPolicyProfile) AutomaticGatewayRecovery(Config) bool { return true }
func (injectedPolicyProfile) GatewayAutomations(Config, CreateGatewayRequest) func(context.Context, int) error {
	return nil
}

func TestManagerUsesInjectedRegistrationAndRecoveryPolicy(t *testing.T) {
	m := NewGatewayManager(Config{RuntimeType: "custom", Runtime: injectedPolicyProfile{}}, &recoveryProcessStarter{}, nil)
	report := m.RegisterPayload()
	if report.ProtocolVersion != "custom-v1" || report.SessionStore != "custom" || len(report.Capabilities) != 0 {
		t.Fatalf("native metadata not injected: %#v", report)
	}
	if !m.automaticGatewayRecoveryEnabled() {
		t.Fatal("injected recovery policy ignored")
	}
}

func TestManagerDoesNotInventNativeRegistrationMetadata(t *testing.T) {
	m := NewGatewayManager(Config{RuntimeType: "custom", OpenClawVersion: "2026.10.2"}, &recoveryProcessStarter{}, nil)
	report := m.RegisterPayload()
	if report.ProtocolVersion != "" || report.SessionStore != "" || len(report.Capabilities) != 0 || report.OpenClawVersion != "" {
		t.Fatalf("core supplied native metadata: %#v", report)
	}
}

type isolatedPolicyProfile struct {
	isolatedTestProfile
	reconciled chan int
}

func (isolatedPolicyProfile) AutomaticGatewayRecovery(Config) bool { return true }
func (p isolatedPolicyProfile) GatewayAutomations(Config, CreateGatewayRequest) func(context.Context, int) error {
	return func(ctx context.Context, port int) error {
		select {
		case p.reconciled <- port:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestIsolatedLifecyclePreservesInjectedAutomationRecoveryAndCommand(t *testing.T) {
	done := make(chan error, 1)
	reconciled := make(chan int, 4)
	second := make(chan struct{}, 1)
	var starts, stops atomic.Int32
	profile := isolatedPolicyProfile{reconciled: reconciled}
	m := isolatedTestManager(t, profile, isolatedTestStarter{start: func(spec GatewayStartSpec) (ManagedProcess, error) {
		n := starts.Add(1)
		if len(spec.Command) != 1 || spec.Command[0] != "custom-command" {
			return ManagedProcess{}, fmt.Errorf("configured command ignored")
		}
		process := ManagedProcess{PID: int(n), Stop: func(context.Context) error { stops.Add(1); return nil }}
		if n == 1 {
			process.Done = done
		} else {
			if stops.Load() < 1 {
				t.Error("restarted before stop confirmation")
			}
			second <- struct{}{}
		}
		return process, nil
	}})
	m.cfg.GatewayCommand = []string{"custom-command"}
	m.restartDelays = []time.Duration{0}
	response, err := m.CreateGateway(context.Background(), isolatedRequest(m, 1, 1, 20000))
	if err != nil {
		t.Fatal(err)
	}
	awaitIsolatedState(t, m, response.GatewayID, "running")
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("isolated automation hook not invoked")
	}
	done <- fmt.Errorf("unexpected exit")
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("isolated recovery hook not invoked")
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("automations not restored after recovery")
	}
}

func TestImageEnvironmentIncludesOnlyExplicitKeys(t *testing.T) {
	t.Setenv("STATIC_RUNTIME_FLAG", "enabled")
	t.Setenv("UNRECOGNIZED_SUPERVISOR_SECRET", "secret")
	env := AppendImageEnvironment([]string{"PATH=/usr/bin"}, "STATIC_RUNTIME_FLAG")
	if EnvironmentValue(env, "STATIC_RUNTIME_FLAG") != "enabled" || EnvironmentValue(env, "UNRECOGNIZED_SUPERVISOR_SECRET") != "" {
		t.Fatalf("unexpected inherited environment: %#v", env)
	}
}

func TestWorkspacePreparationDoesNotChownRedirectedHome(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "custom", "user-1", "instance-2")
	victim := t.TempDir()
	os.MkdirAll(workspace, 0750)
	os.Chmod(victim, 0700)
	os.Symlink(victim, filepath.Join(workspace, "home"))
	_, err := PrepareWorkspace(root, "custom", CreateGatewayRequest{UserID: 1, InstanceID: 2, WorkspacePath: workspace, UID: os.Getuid(), GID: os.Getgid()})
	if err == nil {
		t.Fatal("redirected instance home accepted")
	}
	info, _ := os.Stat(victim)
	if info.Mode().Perm() != 0700 {
		t.Fatal("sibling permissions changed")
	}
}
func TestTeamConfigDoesNotPublishThroughRedirectedSharedDirectory(t *testing.T) {
	workspace := t.TempDir()
	victim := t.TempDir()
	os.Symlink(victim, filepath.Join(workspace, "team"))
	req := CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_CONFIG_JSON": "{}"}}
	if err := WriteLiteTeamConfigJSON(req, workspace); err == nil {
		t.Fatal("redirected Team directory accepted")
	}
	if _, err := os.Stat(filepath.Join(victim, "team.json")); !os.IsNotExist(err) {
		t.Fatal("Team config escaped instance")
	}
}

type credentialProbe struct{ calls atomic.Int32 }

func (p *credentialProbe) WaitReady(context.Context, GatewayStartSpec) error {
	p.calls.Add(1)
	return nil
}
func (p *credentialProbe) CheckHealth(context.Context, GatewayStartSpec) error {
	p.calls.Add(1)
	return nil
}
func TestListenerAttributionPrecedesCredentialHealthProbe(t *testing.T) {
	health := &credentialProbe{}
	m := NewGatewayManager(Config{GatewayStartupTimeout: 5 * time.Millisecond}, &recoveryProcessStarter{}, nil)
	m.SetHealthChecker(health)
	process := ManagedProcess{VerifyListeners: func(context.Context) error { return fmt.Errorf("listener belongs to another slot") }}
	if err := m.waitGatewayReady(context.Background(), GatewayStartSpec{}, process); err == nil {
		t.Fatal("unattributed gateway accepted")
	}
	if health.calls.Load() != 0 {
		t.Fatal("business credentials probed before listener attribution")
	}
}

type injectedUpgradeProfile struct{ runtimeNeutralProfile }
type injectedUpgradeService struct{ RuntimeUpgradeService }

func (injectedUpgradeProfile) UpgradeService(Config, UpgradeHost) RuntimeUpgradeService {
	return injectedUpgradeService{}
}
func (injectedUpgradeService) PreflightWorkspace(context.Context, WorkspaceUpgradeRequest) (WorkspaceInventory, error) {
	return WorkspaceInventory{WorkspacePath: "native-owned"}, nil
}
func TestManagerDelegatesNativeUpgradeToProfile(t *testing.T) {
	manager := NewGatewayManager(Config{RuntimeType: "custom", Runtime: injectedUpgradeProfile{}}, &recoveryProcessStarter{}, nil)
	result, err := manager.PreflightWorkspace(context.Background(), WorkspaceUpgradeRequest{})
	if err != nil || result.WorkspacePath != "native-owned" {
		t.Fatalf("native facade not used: %#v %v", result, err)
	}
}
