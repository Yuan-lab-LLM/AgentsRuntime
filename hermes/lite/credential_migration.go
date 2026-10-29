package hermes

import (
	"bytes"
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"gopkg.in/yaml.v3"
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
	password := values["CLAWMANAGER_INSTANCE_TOKEN"]
	values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] = password
	values["CLAWMANAGER_DASHBOARD_BASIC_AUTH_PASSWORD"] = password
	values["HERMES_DASHBOARD_BASIC_AUTH_SECRET"] = dashboardSigningSecret(password)
	values["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD_HASH"] = ""
	// Inspect all selected generated documents before rewriting any source key,
	// so backups and sibling env aliases can be matched against the old CM key.
	managedKeys := map[string]bool{}
	inspecting := true
	envTransform := func(data []byte) ([]byte, error) {
		if inspecting {
			records := map[string][]string{}
			_, err := managedconfig.EnvironmentWithPolicy(data, nil, func(key, raw string) (string, bool) {
				if value, ok := hermesMigrationEnvLiteral(raw); ok {
					records[key] = append(records[key], value)
				}
				return "", false
			})
			if err != nil {
				return nil, err
			}
			for _, key := range records["CLAWMANAGER_LLM_API_KEY"] {
				if key != "" {
					managedKeys[key] = true
				}
			}
			managedEndpoint := false
			for _, name := range []string{"OPENAI_BASE_URL", "OPENAI_API_BASE", "CUSTOM_BASE_URL", "CLAWMANAGER_LLM_BASE_URL"} {
				for _, endpoint := range records[name] {
					managedEndpoint = managedEndpoint || managedconfig.ManagedModelEndpoint(endpoint)
				}
			}
			if managedEndpoint {
				for _, key := range records["OPENAI_API_KEY"] {
					if key != "" {
						managedKeys[key] = true
					}
				}
			}
		}
		return managedconfig.EnvironmentWithPolicy(data, values, func(key, raw string) (string, bool) {
			switch key {
			case "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY":
				value, ok := hermesMigrationEnvLiteral(raw)
				return managedconfig.ModelKey, ok && value != "" && managedKeys[value]
			}
			return "", false
		})
	}
	yamlTransform := func(data []byte) ([]byte, error) {
		doc, err := parseExistingDesktopWebYAML(data)
		if err != nil {
			return nil, managedconfig.ErrUnsafe
		}
		root := doc.Content[0]
		migrateProvider := func(provider *yaml.Node, managed bool) error {
			if provider == nil {
				return nil
			}
			if provider.Kind != yaml.MappingNode {
				return managedconfig.ErrUnsafe
			}
			base := desktopWebYAMLMappingValue(provider, "base_url")
			if base != nil && base.Kind != yaml.ScalarNode {
				return managedconfig.ErrUnsafe
			}
			if base != nil {
				managed = managedconfig.ManagedModelEndpoint(base.Value)
			}
			if !managed {
				return nil
			}
			if key := desktopWebYAMLMappingValue(provider, "api_key"); key != nil {
				if key.Kind != yaml.ScalarNode {
					return managedconfig.ErrUnsafe
				}
				if inspecting && key.Value != "" {
					managedKeys[key.Value] = true
				}
			}
			for key, value := range map[string]string{"base_url": managedconfig.ModelURL, "api_key": managedconfig.ModelKey, "key_env": "OPENAI_API_KEY", "api_key_env": "OPENAI_API_KEY"} {
				setMigrationYAML(provider, key, value)
			}
			for _, key := range []string{"key_cmd", "api", "url"} {
				if desktopWebYAMLMappingValue(provider, key) != nil {
					setMigrationYAML(provider, key, "")
				}
			}
			return nil
		}
		model := desktopWebYAMLMappingValue(root, "model")
		managedModel := false
		if model != nil {
			if provider := desktopWebYAMLMappingValue(model, "provider"); provider != nil {
				managedModel = provider.Value == "clawmanager" || strings.HasPrefix(provider.Value, "clawmanager-")
			}
		}
		if err = migrateProvider(model, managedModel); err != nil {
			return nil, err
		}
		if providers := desktopWebYAMLMappingValue(root, "providers"); providers != nil {
			if providers.Kind != yaml.MappingNode {
				return nil, managedconfig.ErrUnsafe
			}
			for i := 0; i < len(providers.Content); i += 2 {
				name := providers.Content[i].Value
				if err = migrateProvider(providers.Content[i+1], name == "clawmanager" || strings.HasPrefix(name, "clawmanager-")); err != nil {
					return nil, err
				}
			}
		}
		if err = migrateHermesTeamOverrides(root, req, values); err != nil {
			return nil, err
		}
		if dashboard := desktopWebYAMLMappingValue(root, "dashboard"); dashboard != nil {
			if auth := desktopWebYAMLMappingValue(dashboard, "basic_auth"); auth != nil {
				if auth.Kind != yaml.MappingNode {
					return nil, managedconfig.ErrUnsafe
				}
				for key, value := range map[string]string{"password": password, "password_hash": "", "secret": dashboardSigningSecret(password)} {
					if desktopWebYAMLMappingValue(auth, key) != nil {
						setMigrationYAML(auth, key, value)
					}
				}
			}
		}
		var buffer bytes.Buffer
		encoder := yaml.NewEncoder(&buffer)
		encoder.SetIndent(2)
		if err = encoder.Encode(doc); err != nil {
			return nil, managedconfig.ErrUnsafe
		}
		return buffer.Bytes(), nil
	}
	jsonTransform := func(data []byte) ([]byte, error) {
		config, err := managedconfig.JSON(data)
		if err != nil {
			return nil, err
		}
		if auth, ok := config["auth"].(map[string]any); ok {
			for _, key := range []string{"token", "password"} {
				if _, exists := auth[key]; exists {
					auth[key] = password
				}
			}
		}
		return managedconfig.EncodeJSON(config)
	}
	temporaryTransform := func(data []byte) ([]byte, error) {
		trimmed := bytes.TrimSpace(data)
		if bytes.HasPrefix(trimmed, []byte("{")) {
			return jsonTransform(data)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.Contains(line, "=") {
				return envTransform(data)
			}
			break
		}
		return yamlTransform(data)
	}
	selectFile := func(name string) managedconfig.Transform {
		switch {
		case managedconfig.GeneratedTemporary(name, ""):
			return temporaryTransform
		case managedconfig.Generated(name, "config.yaml") || managedconfig.GeneratedTemporary(name, "config.yaml"):
			return yamlTransform
		case managedconfig.Generated(name, ".env") || managedconfig.GeneratedTemporary(name, ".env"):
			return envTransform
		case managedconfig.Generated(name, "gateway.json") || managedconfig.GeneratedTemporary(name, "gateway.json"):
			return jsonTransform
		}
		return nil
	}
	directories := []managedconfig.Directory{{Path: "home/.hermes", Select: selectFile}, {Path: "home/.clawmanager-team-worker/.hermes", Select: selectFile}}
	inspection := func(name string) managedconfig.Transform {
		transform := selectFile(name)
		if transform == nil {
			return nil
		}
		return func(data []byte) ([]byte, error) { _, err := transform(data); return data, err }
	}
	if err = managedconfig.Rewrite(ctx, cfg.WorkspaceRoot, workspace, []managedconfig.Directory{{Path: directories[0].Path, Select: inspection}, {Path: directories[1].Path, Select: inspection}}); err != nil {
		return err
	}
	inspecting = false
	return managedconfig.Rewrite(ctx, cfg.WorkspaceRoot, workspace, directories)
}
func setMigrationYAML(mapping *yaml.Node, key, value string) {
	if existing := desktopWebYAMLMappingValue(mapping, key); existing != nil {
		*existing = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
		return
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func writeProtectedHermesLegacyConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	if _, err := managedconfig.SafeEnvironment(cfg, req); err != nil {
		return err
	}
	overlay, err := parseDesktopWebYAML([]byte(buildManagedYAML(cfg)))
	if err != nil {
		return err
	}
	yamlTransform := func(data []byte) ([]byte, error) {
		document, err := parseExistingDesktopWebYAML(data)
		if err != nil {
			return nil, err
		}
		mergeDesktopWebYAMLNode(document.Content[0], overlay.Content[0])
		if err := applyManagedVisionYAML(document.Content[0], cfg); err != nil {
			return nil, err
		}
		var buffer bytes.Buffer
		encoder := yaml.NewEncoder(&buffer)
		encoder.SetIndent(2)
		if err = encoder.Encode(document); err != nil {
			return nil, managedconfig.ErrUnsafe
		}
		return buffer.Bytes(), nil
	}
	envTransform := func(data []byte) ([]byte, error) {
		return mergeDesktopWebEnv(data, map[string]string{"OPENAI_API_KEY": cfg.LLMAPIKey, "OPENAI_BASE_URL": cfg.LLMBaseURL, "CUSTOM_BASE_URL": cfg.LLMBaseURL})
	}
	jsonTransform := func(data []byte) ([]byte, error) {
		object := map[string]any{}
		if len(data) > 0 {
			var err error
			object, err = managedconfig.JSON(data)
			if err != nil {
				return nil, err
			}
		}
		origins := cfg.AllowedOrigins
		if len(origins) == 0 && cfg.PublicOrigin != "" {
			origins = []string{cfg.PublicOrigin}
		}
		for key, value := range map[string]any{"base_path": "/api/v1/instances/" + strconv.Itoa(req.InstanceID) + "/proxy", "auth_mode": cfg.GatewayAuthMode, "allowed_origins": origins, "trusted_proxies": cfg.TrustedProxies} {
			object[key] = value
		}
		return managedconfig.EncodeJSON(object)
	}
	transforms := map[string]managedconfig.Transform{"config.yaml": yamlTransform, ".env": envTransform, "gateway.json": jsonTransform}
	written := map[string]bool{}
	if err = managedconfig.Rewrite(context.Background(), cfg.WorkspaceRoot, workspace, []managedconfig.Directory{{Path: "home/.hermes", Select: func(name string) managedconfig.Transform {
		transform := transforms[name]
		if transform == nil {
			return nil
		}
		return func(data []byte) ([]byte, error) {
			out, err := transform(data)
			written[name] = err == nil
			return out, err
		}
	}}}); err != nil {
		return err
	}
	for _, name := range []string{"config.yaml", ".env", "gateway.json"} {
		if written[name] {
			continue
		}
		data, err := transforms[name](nil)
		if err != nil {
			return err
		}
		if err = managedconfig.Create(context.Background(), cfg.WorkspaceRoot, workspace, "home/.hermes", name, data, 0600, req.UID, req.GID); err != nil {
			return err
		}
	}
	return nil
}

// Only literal generated values are evidence; no shell expansion is evaluated.
func hermesMigrationEnvLiteral(raw string) (string, bool) {
	if strings.HasPrefix(raw, "\"") {
		value, err := strconv.Unquote(raw)
		return value, err == nil
	}
	if strings.HasPrefix(raw, "'") {
		if len(raw) < 2 || !strings.HasSuffix(raw, "'") {
			return "", false
		}
		return raw[1 : len(raw)-1], true
	}
	if strings.ContainsAny(raw, "\r\n#$`\\") {
		return "", false
	}
	return raw, true
}

func migrateHermesTeamOverrides(root *yaml.Node, req gateway.CreateGatewayRequest, values map[string]string) error {
	extra := root
	for _, name := range []string{"platforms", "redis_team", "extra"} {
		if extra.Kind != yaml.MappingNode {
			return managedconfig.ErrUnsafe
		}
		extra = desktopWebYAMLMappingValue(extra, name)
		if extra == nil {
			return nil
		}
	}
	if extra.Kind != yaml.MappingNode {
		return managedconfig.ErrUnsafe
	}
	hasCredentials := false
	for _, name := range []string{"redis_url", "redisUrl", "team_token", "teamToken"} {
		hasCredentials = hasCredentials || desktopWebYAMLMappingValue(extra, name) != nil
	}
	if !hasCredentials {
		return nil
	}
	teamID, _ := gateway.RequestEnvValue(req, "CLAWMANAGER_TEAM_ID")
	if teamID == "" {
		return managedconfig.ErrUnsafe
	}
	for _, name := range []string{"team_id", "teamId"} {
		if node := desktopWebYAMLMappingValue(extra, name); node != nil && (node.Kind != yaml.ScalarNode || node.Value != teamID) {
			return managedconfig.ErrUnsafe
		}
	}
	for _, name := range []string{"redis_url", "redisUrl", "team_token", "teamToken"} {
		node := desktopWebYAMLMappingValue(extra, name)
		if node == nil {
			continue
		}
		if node.Kind != yaml.ScalarNode {
			return managedconfig.ErrUnsafe
		}
		value := managedconfig.TeamKey
		if name == "redis_url" || name == "redisUrl" {
			var err error
			value, err = managedconfig.LocalRedis(node.Value, values["CLAWMANAGER_TEAM_REDIS_URL"])
			if err != nil {
				return err
			}
		}
		setMigrationYAML(extra, name, value)
	}
	return nil
}
