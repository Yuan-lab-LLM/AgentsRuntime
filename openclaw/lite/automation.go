package openclaw

import (
	"context"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/scheduledtasks"
	"path/filepath"
	"strings"
)

func (p Profile) AutomaticGatewayRecovery(cfg gateway.Config) bool {
	return p.Type() == "openclaw" && IsOpenClawAtLeast(cfg.OpenClawVersion, OpenClaw81Version)
}
func (p Profile) GatewayAutomations(cfg gateway.Config, req gateway.CreateGatewayRequest) func(context.Context, int) error {
	if cfg.IsolatedLifecycle {
		return p.gatewayAutomations(cfg, req, automationCommandFunc(func(context.Context, []string, []string) ([]byte, error) {
			return nil, fmt.Errorf("isolated automation requires the managed business runner")
		}))
	}
	return p.gatewayAutomations(cfg, req, nil)
}

// ManagedGatewayAutomations never launches the legacy ambient CLI. The manager
// supplies a listener-verified, assigned-identity command executor under its lease.
func (p Profile) ManagedGatewayAutomations(cfg gateway.Config, req gateway.CreateGatewayRequest, execute gateway.GatewayAutomationExecutor) func(context.Context, int) error {
	return p.gatewayAutomations(cfg, req, automationCommandFunc(func(ctx context.Context, args, env []string) ([]byte, error) {
		if execute == nil {
			return nil, fmt.Errorf("managed automation executor is required")
		}
		workspace, err := gateway.ValidateWorkspacePath(cfg.WorkspaceRoot, cfg.RuntimeType, req)
		if err != nil {
			return nil, err
		}
		home := filepath.Join(workspace, "home")
		// CLI bootstrap must resolve the same instance state as the running gateway.
		env = append(append([]string(nil), env...), "HOME="+home, "OPENCLAW_STATE_DIR="+filepath.Join(home, ".openclaw"), "OPENCLAW_CONFIG_PATH="+filepath.Join(home, ".openclaw", "openclaw.json"))
		return execute(ctx, append([]string{"openclaw"}, args...), env)
	}))
}

type automationCommandFunc func(context.Context, []string, []string) ([]byte, error)

func (f automationCommandFunc) Run(ctx context.Context, args, env []string) ([]byte, error) {
	return f(ctx, args, env)
}

func (p Profile) gatewayAutomations(cfg gateway.Config, req gateway.CreateGatewayRequest, runner scheduledtasks.CommandRunner) func(context.Context, int) error {
	if !p.AutomaticGatewayRecovery(cfg) {
		return nil
	}
	_, raw := scheduledtasks.ReadScheduledTasksEnv(func(key string) string {
		if value, ok := req.Environment[key]; ok {
			return value
		}
		return req.Env[key]
	})
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	auth := scheduledtasks.GatewayAuth{Mode: cfg.GatewayAuthMode, Token: cfg.GatewayToken}
	if auth.Mode == "trusted-proxy" {
		auth.Password = strings.TrimSpace(req.Environment["CLAWMANAGER_INSTANCE_TOKEN"])
		if auth.Password == "" {
			auth.Password = strings.TrimSpace(req.Env["CLAWMANAGER_INSTANCE_TOKEN"])
		}
	}
	return func(ctx context.Context, port int) error {
		_, err := scheduledtasks.ReconcileOpenClaw81(ctx, raw, port, auth, runner)
		return err
	}
}
