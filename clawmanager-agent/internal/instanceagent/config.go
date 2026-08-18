package instanceagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Enabled          bool
	BaseURL          string
	BootstrapToken   string
	InstanceID       string
	AgentID          string
	ProtocolVersion  string
	PersistentDir    string
	DiskLimitBytes   int64
	AgentVersion     string
	RuntimeCommand   string
	RuntimeType      string
	HTTPAddr         string
	SkillDirs        []string
	HeartbeatEvery   time.Duration
	CommandPollEvery time.Duration
	StateReportEvery time.Duration
	SkillScanEvery   time.Duration
}

func LoadConfig(version string) (Config, error) {
	persistentDir := getenv("CLAWMANAGER_AGENT_PERSISTENT_DIR", "/config")
	instanceID := os.Getenv("CLAWMANAGER_AGENT_INSTANCE_ID")
	runtimeType := managedRuntimeType()
	runtimeCommand, skillDirs := runtimeDefaults(runtimeType)

	cfg := Config{
		Enabled:          strings.EqualFold(os.Getenv("CLAWMANAGER_AGENT_ENABLED"), "true"),
		BaseURL:          strings.TrimRight(os.Getenv("CLAWMANAGER_AGENT_BASE_URL"), "/"),
		BootstrapToken:   os.Getenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN"),
		InstanceID:       instanceID,
		AgentID:          getenv("CLAWMANAGER_AGENT_ID", fmt.Sprintf("%s-%s-main", runtimeType, fallback(instanceID, "unknown"))),
		ProtocolVersion:  getenv("CLAWMANAGER_AGENT_PROTOCOL_VERSION", "v1"),
		PersistentDir:    persistentDir,
		DiskLimitBytes:   getenvInt64("CLAWMANAGER_AGENT_DISK_LIMIT_BYTES", 0),
		AgentVersion:     version,
		RuntimeCommand:   getenv("CLAWMANAGER_AGENT_COMMAND", getenv("HERMES_COMMAND", runtimeCommand)),
		RuntimeType:      runtimeType,
		HTTPAddr:         normalizeHTTPAddr(os.Getenv("HERMES_AGENT_HTTP_ADDR")),
		SkillDirs:        parseSkillDirs(getenv("CLAWMANAGER_AGENT_SKILL_DIRS", getenv("HERMES_SKILL_DIRS", skillDirs))),
		HeartbeatEvery:   getenvDuration("HERMES_AGENT_HEARTBEAT_INTERVAL_SECONDS", 15*time.Second),
		CommandPollEvery: getenvDuration("HERMES_AGENT_COMMAND_POLL_INTERVAL_SECONDS", 5*time.Second),
		StateReportEvery: getenvDuration("HERMES_AGENT_STATE_INTERVAL_SECONDS", 5*time.Second),
		SkillScanEvery:   getenvDuration("HERMES_AGENT_SKILL_SCAN_INTERVAL_SECONDS", 5*time.Minute),
	}

	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.BaseURL == "" {
		return cfg, errors.New("CLAWMANAGER_AGENT_BASE_URL is required")
	}
	if cfg.BootstrapToken == "" {
		return cfg, errors.New("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN is required")
	}
	if cfg.InstanceID == "" {
		return cfg, errors.New("CLAWMANAGER_AGENT_INSTANCE_ID is required")
	}
	if !filepath.IsAbs(cfg.PersistentDir) {
		return cfg, fmt.Errorf("CLAWMANAGER_AGENT_PERSISTENT_DIR must be absolute: %s", cfg.PersistentDir)
	}
	return cfg, nil
}

func (c Config) WorkDir() string {
	return filepath.Join(c.PersistentDir, c.RuntimeType+"-agent")
}

func managedRuntimeType() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_RUNTIME_TYPE")))
	if value == "" || value == "desktop" || value == "gateway" {
		value = strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_RUNTIME_TYPE")))
	}
	switch value {
	case "opencode", "codex", "claude-code", "hermes":
		return value
	default:
		return "hermes"
	}
}

func runtimeDefaults(runtimeType string) (command, skillDirs string) {
	switch runtimeType {
	case "opencode":
		return "opencode", "/config/.opencode/skills"
	case "codex":
		return "codex", "/config/.codex/skills"
	case "claude-code":
		return "claude", "/config/.claude/skills"
	default:
		return "hermes", "/config/hermes/skills:/config/.hermes/skills"
	}
}

func (c Config) AgentAPIURL(path string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/api/v1/agent") {
		return base + "/" + strings.TrimLeft(path, "/")
	}
	return base + "/api/v1/agent/" + strings.TrimLeft(path, "/")
}

func (c Config) InstanceIDValue() any {
	if id, err := strconv.ParseInt(c.InstanceID, 10, 64); err == nil {
		return id
	}
	return c.InstanceID
}

func getenv(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return def
}

func parseSkillDirs(value string) []string {
	var dirs []string
	for _, part := range strings.Split(value, ":") {
		part = strings.TrimSpace(part)
		if part != "" {
			dirs = append(dirs, part)
		}
	}
	return dirs
}

func normalizeHTTPAddr(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "127.0.0.1:39201"
	}
	switch strings.ToLower(value) {
	case "off", "false", "disabled", "none":
		return ""
	default:
		return value
	}
}

func fallback(value, def string) string {
	if value == "" {
		return def
	}
	return value
}
