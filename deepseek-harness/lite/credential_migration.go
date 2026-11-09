package deepseek

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

// The generic writer is a no-op. start-deepseek-harness-gateway resolves model
// credentials into child environment; the image-owned managed.cordis.patch.yml
// uses apiKeyEnv. No CM-generated credential file exists in DSH_HOME. Preserve
// user Cordis/YAML/env files; new native persistence requires an explicit rule.
func (Profile) MigrateManagedCredentials(ctx context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.NetworkSecurityProfile == "" && req.SecurityProfile == "" {
		return nil
	}
	_, err := managedconfig.SafeEnvironment(cfg, req)
	return err
}
