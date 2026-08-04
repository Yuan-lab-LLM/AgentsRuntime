package hermes

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/scheduledtasks"
)

const managedConfigStart = "# clawmanager-managed-start"
const managedConfigEnd = "# clawmanager-managed-end"

func WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	if err := gateway.WriteLiteTeamConfigJSON(req, workspacePath); err != nil {
		return err
	}
	hermesHome := filepath.Join(workspacePath, "home", ".hermes")
	if err := os.MkdirAll(hermesHome, 0o750); err != nil {
		return fmt.Errorf("create hermes home: %w", err)
	}

	resolved, err := configWithRequestEnv(cfg, req)
	if err != nil {
		return err
	}
	if resolved.LLMAPIKeySet && strings.TrimSpace(resolved.LLMAPIKey) == "" {
		return fmt.Errorf("missing Hermes LLM token")
	}

	if err := writeHermesConfigYAML(filepath.Join(hermesHome, "config.yaml"), resolved); err != nil {
		return err
	}
	if err := writeHermesEnv(filepath.Join(hermesHome, ".env"), resolved); err != nil {
		return err
	}
	if err := writeHermesGatewayConfig(filepath.Join(hermesHome, "gateway.json"), resolved, req); err != nil {
		return err
	}
	result := scheduledtasks.ApplyHermesFromEnv(hermesHome, func(key string) string {
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
	if err := chownHermesHome(hermesHome, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown hermes home: %w", err)
	}
	return nil
}

func chownHermesHome(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return gateway.ChownWorkspace(path, uid, gid)
	})
}

func configWithRequestEnv(cfg gateway.Config, req gateway.CreateGatewayRequest) (gateway.Config, error) {
	resolved := cfg
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"); ok && strings.TrimSpace(value) != "" {
		resolved.LLMBaseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	}
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"); ok {
		resolved.LLMAPIKey = value
		resolved.LLMAPIKeySet = true
	}
	if raw, ok := requestEnvValue(req, "CLAWMANAGER_LLM_MODEL", "OPENAI_MODEL"); ok && strings.TrimSpace(raw) != "" {
		modelIDs, err := parseModelIDs(raw)
		if err != nil {
			return gateway.Config{}, err
		}
		resolved.LLMModelIDs = modelIDs
	}
	return resolved, nil
}

func writeHermesConfigYAML(path string, cfg gateway.Config) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hermes config: %w", err)
	}
	base := stripManagedBlock(string(existing))
	block := buildManagedYAML(cfg)
	content := strings.TrimRight(base, "\r\n")
	if content != "" {
		content += "\n\n"
	}
	content += block
	return writeFileAtomic(path, []byte(content), 0o600)
}

func buildManagedYAML(cfg gateway.Config) string {
	defaultModel := ""
	if len(cfg.LLMModelIDs) > 0 {
		defaultModel = cfg.LLMModelIDs[0]
	}
	var builder strings.Builder
	builder.WriteString(managedConfigStart)
	builder.WriteString("\nmodel:\n")
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

func writeHermesEnv(path string, cfg gateway.Config) error {
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
	if data, err := os.ReadFile(path); err == nil {
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
	return writeFileAtomic(path, []byte(builder.String()), 0o600)
}

func writeHermesGatewayConfig(path string, cfg gateway.Config, req gateway.CreateGatewayRequest) error {
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
	return writeFileAtomic(path, append(payload, '\n'), 0o600)
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

func parseModelIDs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var parsed []any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			modelIDs := parseDelimitedModelIDs(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
			if len(modelIDs) == 0 {
				return nil, fmt.Errorf("parse CLAWMANAGER_LLM_MODEL array: %w", err)
			}
			return modelIDs, nil
		}
		return uniqueModelIDs(parsed), nil
	}
	return []string{raw}, nil
}

func parseDelimitedModelIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]any, 0, len(parts))
	for _, part := range parts {
		id := strings.Trim(strings.TrimSpace(part), `"'`)
		if id != "" {
			values = append(values, id)
		}
	}
	return uniqueModelIDs(values)
}

func uniqueModelIDs(values []any) []string {
	seen := map[string]bool{}
	modelIDs := []string{}
	for _, value := range values {
		id := strings.TrimSpace(fmt.Sprint(value))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		modelIDs = append(modelIDs, id)
	}
	return modelIDs
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

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod file: %w", err)
	}
	return nil
}
