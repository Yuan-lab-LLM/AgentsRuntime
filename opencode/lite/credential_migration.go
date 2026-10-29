package opencode

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
)

func (Profile) MigrateManagedCredentials(ctx context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	if cfg.NetworkSecurityProfile == "" && req.SecurityProfile == "" {
		return nil
	}
	if _, err := managedconfig.SafeEnvironment(cfg, req); err != nil {
		return err
	}
	transform := func(data []byte) ([]byte, error) {
		config, err := managedconfig.JSON(data)
		if err != nil {
			return nil, err
		}
		if _, err = managedconfig.ObjectPath(config, "provider"); err != nil {
			return nil, err
		}
		if providers, ok := config["provider"].(map[string]any); ok {
			for id, raw := range providers {
				provider, ok := raw.(map[string]any)
				if !ok {
					return nil, managedconfig.ErrUnsafe
				}
				if _, err = managedconfig.ObjectPath(provider, "options"); err != nil {
					return nil, err
				}
				if options, ok := provider["options"].(map[string]any); ok {
					base, _ := options["baseURL"].(string)
					if managedconfig.ManagedModelEndpoint(base) || (id == clawmanagerProviderID && base == "") {
						options["baseURL"] = managedconfig.ModelURL
						options["apiKey"] = "{env:CLAWMANAGER_LLM_API_KEY}"
					}
				}
			}
		}
		return managedconfig.EncodeJSON(config)
	}
	return managedconfig.Rewrite(ctx, cfg.WorkspaceRoot, workspace, []managedconfig.Directory{{Path: "home/.opencode", Select: func(name string) managedconfig.Transform {
		if managedconfig.Generated(name, "opencode.json") || managedconfig.GeneratedTemporary(name, "") {
			return transform
		}
		return nil
	}}})
}

func writeProtectedOpenCodeConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string, raw []byte) error {
	if _, err := managedconfig.SafeEnvironment(cfg, req); err != nil {
		return err
	}
	overlay, err := managedconfig.JSON(raw)
	if err != nil {
		return err
	}
	written := false
	transform := func(existing []byte) ([]byte, error) {
		base, err := managedconfig.JSON(existing)
		if err != nil {
			return nil, err
		}
		mergeOpenCodeManagedObject(base, overlay)
		written = true
		return managedconfig.EncodeJSON(base)
	}
	err = managedconfig.Rewrite(context.Background(), cfg.WorkspaceRoot, workspace, []managedconfig.Directory{{Path: "home/.opencode", Select: func(name string) managedconfig.Transform {
		if name == "opencode.json" {
			return transform
		}
		return nil
	}}})
	if err != nil {
		return err
	}
	if written {
		return nil
	}
	return managedconfig.Create(context.Background(), cfg.WorkspaceRoot, workspace, "home/.opencode", "opencode.json", raw, 0640, req.UID, req.GID)
}
func mergeOpenCodeManagedObject(destination, source map[string]any) {
	for key, value := range source {
		if child, ok := value.(map[string]any); ok {
			if current, ok := destination[key].(map[string]any); ok {
				mergeOpenCodeManagedObject(current, child)
				continue
			}
		}
		destination[key] = value
	}
}
