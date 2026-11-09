package openclaw

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

func (Profile) MigrateManagedCredentials(ctx context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	if cfg.NetworkSecurityProfile == "" && req.SecurityProfile == "" {
		return nil
	}
	values, err := managedconfig.SafeEnvironment(cfg, req)
	if err != nil {
		return err
	}
	for key, want := range map[string]string{"OPENCLAW_STATE_DIR": filepath.Join(workspace, "home", ".openclaw"), "OPENCLAW_CONFIG_PATH": filepath.Join(workspace, "home", ".openclaw", "openclaw.json")} {
		if value, exists := gateway.RequestEnvValue(req, key); exists && value != "" && filepath.Clean(value) != want {
			return managedconfig.ErrUnsafe
		}
	}
	if _, err = resolveOpenClawInstancePaths(req, workspace); err != nil {
		return managedconfig.ErrUnsafe
	}
	transform := func(raw []byte) ([]byte, error) {
		config, err := managedconfig.JSON(raw)
		if err != nil {
			return nil, err
		}
		for _, keys := range [][]string{{"models", "providers"}, {"gateway", "auth"}, {"env"}, {"channels", "redis-team", "accounts", "default"}} {
			if _, err = managedconfig.ObjectPath(config, keys...); err != nil {
				return nil, err
			}
		}
		if providers, err := managedconfig.ObjectPath(config, "models", "providers"); err != nil {
			return nil, err
		} else if err = migrateOpenClawModelProviders(providers); err != nil {
			return nil, err
		}
		if native, ok := config["gateway"].(map[string]any); ok {
			if auth, ok := native["auth"].(map[string]any); ok {
				for _, key := range []string{"token", "password"} {
					if _, exists := auth[key]; exists {
						auth[key] = values["CLAWMANAGER_INSTANCE_TOKEN"]
					}
				}
			}
		}
		if env, ok := config["env"].(map[string]any); ok {
			for key, value := range env {
				if replacement, managed := values[key]; managed {
					if strings.HasSuffix(key, "REDIS_URL") {
						previous, ok := value.(string)
						if !ok {
							return nil, managedconfig.ErrUnsafe
						}
						replacement, err = managedconfig.LocalRedis(previous, replacement)
						if err != nil {
							return nil, err
						}
					}
					env[key] = replacement
				}
			}
		}
		if channels, ok := config["channels"].(map[string]any); ok {
			if team, ok := channels["redis-team"].(map[string]any); ok {
				if accounts, ok := team["accounts"].(map[string]any); ok {
					if account, ok := accounts["default"].(map[string]any); ok {
						if old, exists := account["redisUrl"]; exists {
							previous, ok := old.(string)
							if !ok {
								return nil, managedconfig.ErrUnsafe
							}
							account["redisUrl"], err = managedconfig.LocalRedis(previous, values["CLAWMANAGER_TEAM_REDIS_URL"])
							if err != nil {
								return nil, err
							}
						}
						for _, key := range []string{"teamToken", "token"} {
							if _, exists := account[key]; exists {
								account[key] = managedconfig.TeamKey
							}
						}
					}
				}
			}
		}
		migrateBrowserURLs(config, req.InstanceID)
		return managedconfig.EncodeJSON(config)
	}
	modelDirectories, err := openClawModelStateDirectories(ctx, cfg, req, workspace)
	if err != nil {
		return err
	}
	directories := []managedconfig.Directory{{Path: "home/.openclaw", Select: func(name string) managedconfig.Transform {
		if openClawGeneratedConfig(name) {
			return transform
		}
		return nil
	}}}
	modelTransform := func(raw []byte) ([]byte, error) {
		config, err := managedconfig.JSON(raw)
		if err != nil {
			return nil, err
		}
		providers, err := managedconfig.ObjectPath(config, "providers")
		if err != nil {
			return nil, err
		}
		if err = migrateOpenClawModelProviders(providers); err != nil {
			return nil, err
		}
		return managedconfig.EncodeJSON(config)
	}
	for _, directory := range modelDirectories {
		directories = append(directories, managedconfig.Directory{Path: directory, Select: func(name string) managedconfig.Transform {
			if name == "models.json" || openClawModelsTemporary(name) {
				return modelTransform
			}
			return nil
		}})
	}
	return managedconfig.Rewrite(ctx, cfg.WorkspaceRoot, workspace, directories)
}

func migrateOpenClawModelProviders(providers map[string]any) error {
	for id, raw := range providers {
		provider, ok := raw.(map[string]any)
		if !ok {
			return managedconfig.ErrUnsafe
		}
		base := ""
		if value, exists := provider["baseUrl"]; exists {
			base, ok = value.(string)
			if !ok {
				return managedconfig.ErrUnsafe
			}
		}
		if managedconfig.ManagedModelEndpoint(base) || ((id == "auto" || id == "clawmanager") && base == "") {
			provider["baseUrl"] = managedconfig.ModelURL
			provider["apiKey"] = managedconfig.ModelKey
		}
	}
	return nil
}
func openClawModelStateDirectories(ctx context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) ([]string, error) {
	names, err := managedconfig.Subdirectories(ctx, cfg.WorkspaceRoot, workspace, "home/.openclaw/agents")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var directories []string
	add := func(directory string) error {
		relative, err := filepath.Rel(workspace, directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return managedconfig.ErrUnsafe
		}
		if seen[relative] {
			return nil
		}
		if len(directories) >= managedconfig.MaxFiles {
			return managedconfig.ErrUnsafe
		}
		seen[relative] = true
		directories = append(directories, relative)
		return nil
	}
	for _, key := range []string{"OPENCLAW_AGENT_DIR", "PI_CODING_AGENT_DIR"} {
		if directory, exists := gateway.RequestEnvValue(req, key); exists && directory != "" {
			if strings.HasPrefix(directory, "~/") {
				directory = filepath.Join(workspace, "home", strings.TrimPrefix(directory, "~/"))
			}
			if !filepath.IsAbs(directory) {
				return nil, managedconfig.ErrUnsafe
			}
			if err = add(filepath.Clean(directory)); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range names {
		if err = add(filepath.Join(workspace, "home", ".openclaw", "agents", name, "agent")); err != nil {
			return nil, err
		}
	}
	inspect := func(data []byte) ([]byte, error) {
		config, err := managedconfig.JSON(data)
		if err != nil {
			return nil, err
		}
		agents, err := managedconfig.ObjectPath(config, "agents")
		if err != nil {
			return nil, err
		}
		raw, exists := agents["list"]
		if !exists {
			return data, nil
		}
		list, ok := raw.([]any)
		if !ok || len(list) > managedconfig.MaxFiles {
			return nil, managedconfig.ErrUnsafe
		}
		for _, raw := range list {
			agent, ok := raw.(map[string]any)
			if !ok {
				return nil, managedconfig.ErrUnsafe
			}
			rawDirectory, exists := agent["agentDir"]
			if !exists {
				continue
			}
			directory, ok := rawDirectory.(string)
			if !ok || strings.TrimSpace(directory) != directory || directory == "" {
				return nil, managedconfig.ErrUnsafe
			}
			if strings.HasPrefix(directory, "~/") {
				directory = filepath.Join(workspace, "home", strings.TrimPrefix(directory, "~/"))
			}
			if !filepath.IsAbs(directory) {
				return nil, managedconfig.ErrUnsafe
			}
			if err = add(filepath.Clean(directory)); err != nil {
				return nil, err
			}
		}
		return data, nil
	}
	err = managedconfig.Rewrite(ctx, cfg.WorkspaceRoot, workspace, []managedconfig.Directory{{Path: "home/.openclaw", Select: func(name string) managedconfig.Transform {
		if openClawGeneratedConfig(name) {
			return inspect
		}
		return nil
	}}})
	return directories, err
}

func migrateBrowserURLs(value any, instanceID int) any {
	switch v := value.(type) {
	case string:
		u, err := url.Parse(v)
		if err != nil {
			return value
		}
		labels := strings.Split(u.Hostname(), ".")
		if (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "ws" || u.Scheme == "wss") && u.Port() == "9222" && (len(labels) == 3 || len(labels) == 5) && labels[0] == "clawbrowser-"+strconv.Itoa(instanceID) && labels[1] != "" && labels[2] == "svc" && (len(labels) == 3 || (labels[3] == "cluster" && labels[4] == "local")) {
			return managedconfig.ServicesURL
		}
	case map[string]any:
		for key, child := range v {
			v[key] = migrateBrowserURLs(child, instanceID)
		}
	case []any:
		for i, child := range v {
			v[i] = migrateBrowserURLs(child, instanceID)
		}
	}
	return value
}

// @openclaw/fs-safe 0.24.2 uses this exact UUID temporary name for models.json.
func openClawModelsTemporary(name string) bool {
	if !strings.HasPrefix(name, ".fs-safe-") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, ".fs-safe-"), ".tmp")
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func openClawGeneratedConfig(name string) bool {
	return managedconfig.Generated(name, "openclaw.json") || managedconfig.GeneratedTemporary(name, "") || name == "openclaw.json.browser-worker-state" || strings.HasPrefix(name, ".browser-worker-")
}
