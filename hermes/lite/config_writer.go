package hermes

import (
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

const managedConfigStart = "# clawmanager-managed-start"
const managedConfigEnd = "# clawmanager-managed-end"

func WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	if desktopWebEnabled() {
		// Validate and persist the Dashboard's managed configuration first. An
		// invalid Web request must not mutate the shared Team contract. Both
		// writers are idempotent, so a later Team filesystem error can converge
		// safely on retry without replacing user state.
		if err := writeDesktopWebConfig(cfg, req, workspacePath); err != nil {
			return err
		}
		return gateway.WriteLiteTeamConfigJSON(req, workspacePath)
	}
	if err := gateway.WriteLiteTeamConfigJSON(req, workspacePath); err != nil {
		return err
	}
	root, err := workspaceio.Ensure(workspacePath, 0750)
	if err != nil {
		return err
	}
	defer root.Close()
	hermesHome := filepath.Join(workspacePath, "home", ".hermes")
	if err := root.MkdirAll(hermesHome, 0o750); err != nil {
		return fmt.Errorf("create hermes home: %w", err)
	}

	resolved, err := configWithRequestEnv(cfg, req)
	if err != nil {
		return err
	}
	if resolved.LLMAPIKeySet && strings.TrimSpace(resolved.LLMAPIKey) == "" {
		return fmt.Errorf("missing Hermes LLM token")
	}

	protected := cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile
	if protected {
		if err := writeProtectedHermesLegacyConfig(resolved, req, workspacePath); err != nil {
			return err
		}
		// New generated directories must be traversable by the native instance;
		// pin the directory inode instead of chowning its unselected descendants.
		pinned, err := workspaceio.OpenAt(cfg.WorkspaceRoot, hermesHome)
		if err != nil {
			return err
		}
		directory, err := pinned.Open(".")
		if err == nil {
			err = directory.Chown(req.UID, req.GID)
			directory.Close()
		}
		pinned.Close()
		if err != nil {
			return err
		}

	} else {
		if err := writeHermesConfigYAML(root, filepath.Join(hermesHome, "config.yaml"), resolved); err != nil {
			return err
		}
		if err := writeHermesEnv(root, filepath.Join(hermesHome, ".env"), resolved); err != nil {
			return err
		}
		if err := writeHermesGatewayConfig(root, filepath.Join(hermesHome, "gateway.json"), resolved, req); err != nil {
			return err
		}

	}
	result := applyHermesTasks(root, hermesHome, func(key string) string {
		if req.Environment != nil {
			if value, ok := req.Environment[key]; ok {
				return value
			}
		}
		if req.Env != nil {
			if value, ok := req.Env[key]; ok {
				return value
			}
		}
		return os.Getenv(key)
	}, req.UID, req.GID)
	if result.Error != "" {
		log.Printf("apply hermes scheduled tasks failed (continuing): source_env=%s error=%s", result.SourceEnv, result.Error)
	}
	if !protected {
		if err := root.ChownTree(hermesHome, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown hermes home: %w", err)
		}
	}
	return nil
}

func configWithRequestEnv(cfg gateway.Config, req gateway.CreateGatewayRequest) (gateway.Config, error) {
	settings, err := llmconfig.ResolveGateway(cfg, req, llmconfig.ResolveOptions{})
	if err != nil {
		return gateway.Config{}, err
	}
	return settings.ApplyTo(cfg), nil
}

func writeHermesConfigYAML(root *workspaceio.FS, path string, cfg gateway.Config) error {
	existing, err := root.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hermes config: %w", err)
	}
	base := stripManagedBlock(string(existing))
	base, err = mergeManagedVisionYAML(base, cfg)
	if err != nil {
		return err
	}
	block := buildManagedYAML(cfg)
	content := strings.TrimRight(base, "\r\n")
	if content != "" {
		content += "\n\n"
	}
	content += block
	return writeFileAtomic(root, path, []byte(content), 0o600)
}

func buildManagedYAML(cfg gateway.Config) string {
	defaultModel := ""
	if len(cfg.LLMModelIDs) > 0 {
		defaultModel = cfg.LLMModelIDs[0]
	}
	var builder strings.Builder
	builder.WriteString(managedConfigStart)
	builder.WriteString("\nmodel:\n")
	if cfg.LLMModelsQualified {
		groups := llmconfig.GroupModelRefs(llmconfig.FromGatewayConfig(cfg).ModelRefs("auto"))
		if len(groups) > 0 {
			defaultModel = groups[0].ModelIDs[0]
			builder.WriteString("  default: ")
			builder.WriteString(yamlScalar(defaultModel))
			builder.WriteString("\n")
			builder.WriteString("  provider: ")
			builder.WriteString(yamlScalar(groups[0].ProviderID))
			builder.WriteString("\n")
			if cfg.LLMBaseURL != "" {
				builder.WriteString("providers:\n")
				for _, group := range groups {
					builder.WriteString("  ")
					builder.WriteString(yamlScalar(group.ProviderID))
					builder.WriteString(":\n")
					writeManagedProviderYAML(&builder, group.ProviderID, cfg.LLMBaseURL, group.ModelIDs[0], group.ModelIDs)
				}
			}
		}
		builder.WriteString(managedConfigEnd)
		builder.WriteString("\n")
		return builder.String()
	}
	if defaultModel != "" {
		builder.WriteString("  default: ")
		builder.WriteString(yamlScalar(defaultModel))
		builder.WriteString("\n")
	}
	if cfg.LLMBaseURL != "" {
		builder.WriteString("  provider: clawmanager\n")
		builder.WriteString("providers:\n")
		builder.WriteString("  clawmanager:\n")
		writeManagedProviderYAML(&builder, "ClawManager", cfg.LLMBaseURL, defaultModel, cfg.LLMModelIDs)
		builder.WriteString("  custom:\n")
		writeManagedProviderYAML(&builder, "custom", cfg.LLMBaseURL, defaultModel, cfg.LLMModelIDs)
	}
	builder.WriteString(managedConfigEnd)
	builder.WriteString("\n")
	return builder.String()
}

func writeManagedProviderYAML(builder *strings.Builder, name, baseURL, defaultModel string, modelIDs []string) {
	builder.WriteString("    name: ")
	builder.WriteString(yamlScalar(name))
	builder.WriteString("\n")
	builder.WriteString("    base_url: ")
	builder.WriteString(yamlScalar(baseURL))
	builder.WriteString("\n")
	if defaultModel != "" {
		builder.WriteString("    default_model: ")
		builder.WriteString(yamlScalar(defaultModel))
		builder.WriteString("\n")
	}
	builder.WriteString("    transport: openai_chat\n")
	builder.WriteString("    key_env: OPENAI_API_KEY\n")
	if len(modelIDs) > 0 {
		builder.WriteString("    models:\n")
		for _, modelID := range modelIDs {
			builder.WriteString("      ")
			builder.WriteString(yamlScalar(modelID))
			builder.WriteString(": {}\n")
		}
	}
}

func writeHermesEnv(root *workspaceio.FS, path string, cfg gateway.Config) error {
	values := map[string]string{}
	if cfg.LLMAPIKeySet {
		values["OPENAI_API_KEY"] = cfg.LLMAPIKey
	}
	if cfg.LLMBaseURL != "" {
		values["OPENAI_BASE_URL"] = cfg.LLMBaseURL
		values["CUSTOM_BASE_URL"] = cfg.LLMBaseURL
	}
	if len(values) == 0 {
		return nil
	}
	existing := map[string]string{}
	if data, err := root.ReadFile(path); err == nil {
		existing = parseEnvFile(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read hermes env: %w", err)
	}
	for key, value := range values {
		existing[key] = value
	}
	keys := []string{"CUSTOM_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL"}
	var builder strings.Builder
	for _, key := range keys {
		if value, ok := existing[key]; ok {
			builder.WriteString(key)
			builder.WriteString("=")
			builder.WriteString(quoteEnvValue(value))
			builder.WriteString("\n")
		}
	}
	return writeFileAtomic(root, path, []byte(builder.String()), 0o600)
}

func writeHermesGatewayConfig(root *workspaceio.FS, path string, cfg gateway.Config, req gateway.CreateGatewayRequest) error {
	origins := cfg.AllowedOrigins
	if len(origins) == 0 && cfg.PublicOrigin != "" {
		origins = []string{cfg.PublicOrigin}
	}
	data := map[string]any{
		"base_path":       "/api/v1/instances/" + strconv.Itoa(req.InstanceID) + "/proxy",
		"auth_mode":       cfg.GatewayAuthMode,
		"allowed_origins": origins,
		"trusted_proxies": cfg.TrustedProxies,
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hermes gateway config: %w", err)
	}
	return writeFileAtomic(root, path, append(payload, '\n'), 0o600)
}

func stripManagedBlock(content string) string {
	start := strings.Index(content, managedConfigStart)
	if start < 0 {
		return content
	}
	end := strings.Index(content[start:], managedConfigEnd)
	if end < 0 {
		return strings.TrimRight(content[:start], "\r\n") + "\n"
	}
	end += start + len(managedConfigEnd)
	for end < len(content) && (content[end] == '\r' || content[end] == '\n') {
		end++
	}
	return content[:start] + content[end:]
}

func parseEnvFile(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return values
}

func yamlScalar(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, "#\n\r") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return strconv.Quote(value)
	}
	return value
}

func quoteEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t\r\n\"'#$`\\") {
		return strconv.Quote(value)
	}
	return value
}

func writeFileAtomic(root *workspaceio.FS, path string, data []byte, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return root.WriteFileAtomic(path, data, mode)
}
