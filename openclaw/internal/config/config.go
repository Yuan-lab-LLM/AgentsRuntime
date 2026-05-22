package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath = "/etc/openclaw-agent/config.yaml"
)

type Config struct {
	Enabled                          bool          `yaml:"enabled"`
	RuntimeType                      string        `yaml:"runtime_type"`
	RuntimeName                      string        `yaml:"runtime_name"`
	RuntimeImage                     string        `yaml:"runtime_image"`
	DesktopBase                      string        `yaml:"desktop_base"`
	RuntimePort                      int           `yaml:"runtime_port"`
	InstanceID                       string        `yaml:"instance_id"`
	BootstrapToken                   string        `yaml:"bootstrap_token"`
	ControlPlaneBaseURL              string        `yaml:"control_plane_base_url"`
	AgentDataDir                     string        `yaml:"agent_data_dir"`
	DiskUsagePath                    string        `yaml:"disk_usage_path"`
	DiskLimitBytes                   uint64        `yaml:"disk_limit_bytes"`
	InitialConfigRevisionID          string        `yaml:"initial_config_revision_id"`
	ProtocolVersion                  string        `yaml:"protocol_version"`
	LocalHTTPBind                    string        `yaml:"local_http_bind"`
	LogFilePath                      string        `yaml:"log_file_path"`
	OpenClawCommand                  []string      `yaml:"openclaw_command"`
	OpenClawDoctorCommand            []string      `yaml:"openclaw_doctor_command"`
	OpenClawDoctorPolicy             string        `yaml:"openclaw_doctor_policy"`
	StartupNotificationMessage       string        `yaml:"startup_notification_message"`
	StartupRepairNotificationMessage string        `yaml:"startup_repair_notification_message"`
	OpenClawConfigPath               string        `yaml:"openclaw_config_path"`
	OpenClawWorkspacePath            string        `yaml:"openclaw_workspace_path"`
	OpenClawSkillsPath               string        `yaml:"openclaw_skills_path"`
	OpenClawBuiltinSkillsPath        string        `yaml:"openclaw_builtin_skills_path"`
	OpenClawHealthURL                string        `yaml:"openclaw_health_url"`
	OpenClawStartupHealthTimeoutRaw  string        `yaml:"openclaw_startup_health_timeout"`
	OpenClawDefaultsDir              string        `yaml:"openclaw_defaults_dir"`
	AutostartDefaultsDir             string        `yaml:"autostart_defaults_dir"`
	AutostartTargetDir               string        `yaml:"autostart_target_dir"`
	OpenClawExtensionsDir            string        `yaml:"openclaw_extensions_dir"`
	OpenClawBundledExtensionsDir     string        `yaml:"openclaw_bundled_extensions_dir"`
	InstalledPluginPathPrefix        string        `yaml:"installed_plugin_path_prefix"`
	DropUserName                     string        `yaml:"drop_user_name"`
	BrowserAutoLaunchEnabled         bool          `yaml:"browser_auto_launch_enabled"`
	BrowserExecutable                string        `yaml:"browser_executable"`
	BrowserURL                       string        `yaml:"browser_url"`
	WaylandSocketPath                string        `yaml:"wayland_socket_path"`
	BrowserLaunchWaylandTimeoutRaw   string        `yaml:"browser_launch_wayland_timeout"`
	BrowserLaunchExtraDelayRaw       string        `yaml:"browser_launch_extra_delay"`
	BrowserLaunchWaylandTimeout      time.Duration `yaml:"-"`
	BrowserLaunchExtraDelay          time.Duration `yaml:"-"`
	OpenClawStartupHealthTimeout     time.Duration `yaml:"-"`
	HeartbeatInterval                time.Duration `yaml:"-"`
	StateReportInterval              time.Duration `yaml:"-"`
	CommandPollInterval              time.Duration `yaml:"-"`
	CommandPollBackoffMax            time.Duration `yaml:"-"`
	RegisterRetryInterval            time.Duration `yaml:"-"`
	ProcessStopTimeout               time.Duration `yaml:"-"`
	SkillIncrementalInterval         time.Duration `yaml:"-"`
	SkillFullSyncInterval            time.Duration `yaml:"-"`
	MaxAutoRestart                   int           `yaml:"max_auto_restart"`
	HeartbeatIntervalRaw             string        `yaml:"heartbeat_interval"`
	StateReportIntervalRaw           string        `yaml:"state_report_interval"`
	CommandPollIntervalRaw           string        `yaml:"command_poll_interval"`
	CommandPollBackoffMaxRaw         string        `yaml:"command_poll_backoff_max"`
	RegisterRetryIntervalRaw         string        `yaml:"register_retry_interval"`
	ProcessStopTimeoutRaw            string        `yaml:"process_stop_timeout"`
	SkillIncrementalRaw              string        `yaml:"skill_incremental_interval"`
	SkillFullSyncRaw                 string        `yaml:"skill_full_sync_interval"`
}

func Load() (Config, error) {
	persistentDir := envOrDefault("CLAWMANAGER_AGENT_PERSISTENT_DIR", "/config")
	cfg := Config{
		Enabled:                          strings.EqualFold(envFirst("OPENCLAW_AGENT_ENABLED", "CLAWMANAGER_AGENT_ENABLED"), "true"),
		RuntimeType:                      "openclaw",
		RuntimeName:                      "OpenClaw Desktop",
		DesktopBase:                      "webtop",
		RuntimePort:                      3001,
		AgentDataDir:                     filepath.Join(persistentDir, "openclaw-agent"),
		DiskUsagePath:                    persistentDir,
		ProtocolVersion:                  "v1",
		LocalHTTPBind:                    "0.0.0.0:18080",
		LogFilePath:                      "/var/log/openclaw-agent/agent.log",
		OpenClawCommand:                  []string{"openclaw", "gateway", "run"},
		OpenClawDoctorCommand:            []string{"openclaw", "doctor", "--fix"},
		OpenClawDoctorPolicy:             "auto",
		StartupNotificationMessage:       "正在启动龙虾",
		StartupRepairNotificationMessage: "正在修复启动环境，可能需要 1-2 分钟",
		OpenClawConfigPath:               "/config/.openclaw/openclaw.json",
		OpenClawWorkspacePath:            "/config/.openclaw/workspace",
		OpenClawSkillsPath:               "/config/.openclaw/workspace/skills",
		OpenClawBuiltinSkillsPath:        "/usr/lib/node_modules/openclaw/skills",
		OpenClawHealthURL:                "http://127.0.0.1:18789/health",
		OpenClawStartupHealthTimeoutRaw:  "90s",
		OpenClawDefaultsDir:              "/defaults/.openclaw",
		AutostartDefaultsDir:             "/defaults/.config/autostart",
		AutostartTargetDir:               "/config/.config/autostart",
		OpenClawExtensionsDir:            "/config/.openclaw/extensions",
		OpenClawBundledExtensionsDir:     "/usr/local/lib/node_modules/openclaw/dist/extensions",
		InstalledPluginPathPrefix:        "/defaults/.openclaw/extensions/",
		DropUserName:                     "abc",
		BrowserAutoLaunchEnabled:         true,
		BrowserExecutable:                "/usr/local/bin/wrapped-chromium",
		BrowserURL:                       "http://localhost:18789",
		WaylandSocketPath:                "/config/.XDG/wayland-0",
		BrowserLaunchWaylandTimeoutRaw:   "60s",
		BrowserLaunchExtraDelayRaw:       "0s",
		HeartbeatIntervalRaw:             "15s",
		StateReportIntervalRaw:           "5s",
		CommandPollIntervalRaw:           "5s",
		CommandPollBackoffMaxRaw:         "60s",
		RegisterRetryIntervalRaw:         "10s",
		ProcessStopTimeoutRaw:            "20s",
		SkillIncrementalRaw:              "30s",
		SkillFullSyncRaw:                 "12h",
		MaxAutoRestart:                   3,
	}

	path := envOrDefault("OPENCLAW_AGENT_CONFIG_PATH", defaultConfigPath)
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	overrideBoolAny(&cfg.Enabled, "OPENCLAW_AGENT_ENABLED", "CLAWMANAGER_AGENT_ENABLED")
	overrideStringAny(&cfg.RuntimeType, "OPENCLAW_AGENT_RUNTIME_TYPE", "CLAWMANAGER_AGENT_RUNTIME_TYPE", "CLAWMANAGER_RUNTIME_TYPE")
	overrideStringAny(&cfg.RuntimeName, "OPENCLAW_AGENT_RUNTIME_NAME", "CLAWMANAGER_AGENT_RUNTIME_NAME", "CLAWMANAGER_RUNTIME_NAME")
	overrideStringAny(&cfg.RuntimeImage, "OPENCLAW_AGENT_RUNTIME_IMAGE", "CLAWMANAGER_AGENT_RUNTIME_IMAGE", "CLAWMANAGER_RUNTIME_IMAGE")
	overrideStringAny(&cfg.DesktopBase, "OPENCLAW_AGENT_DESKTOP_BASE", "CLAWMANAGER_AGENT_DESKTOP_BASE")
	overrideIntAny(&cfg.RuntimePort, "OPENCLAW_AGENT_RUNTIME_PORT", "CLAWMANAGER_AGENT_RUNTIME_PORT")
	overrideStringAny(&cfg.InstanceID, "OPENCLAW_AGENT_INSTANCE_ID", "CLAWMANAGER_AGENT_INSTANCE_ID", "INSTANCE_ID")
	overrideStringAny(&cfg.BootstrapToken, "OPENCLAW_AGENT_BOOTSTRAP_TOKEN", "CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN")
	overrideStringAny(&cfg.ControlPlaneBaseURL, "OPENCLAW_AGENT_CONTROL_PLANE_BASE_URL", "CLAWMANAGER_AGENT_BASE_URL")
	overrideStringAny(&cfg.AgentDataDir, "OPENCLAW_AGENT_DATA_DIR", "CLAWMANAGER_AGENT_DATA_DIR")
	overrideStringAny(&cfg.DiskUsagePath, "OPENCLAW_AGENT_DISK_USAGE_PATH", "CLAWMANAGER_AGENT_DISK_USAGE_PATH", "CLAWMANAGER_AGENT_PERSISTENT_DIR")
	overrideStringAny(&cfg.InitialConfigRevisionID, "OPENCLAW_AGENT_INITIAL_CONFIG_REVISION_ID")
	overrideStringAny(&cfg.ProtocolVersion, "OPENCLAW_AGENT_PROTOCOL_VERSION", "CLAWMANAGER_AGENT_PROTOCOL_VERSION")
	overrideStringAny(&cfg.LocalHTTPBind, "OPENCLAW_AGENT_LOCAL_HTTP_BIND")
	overrideStringAny(&cfg.LogFilePath, "OPENCLAW_AGENT_LOG_FILE_PATH")
	overrideStringAny(&cfg.StartupNotificationMessage, "OPENCLAW_AGENT_STARTUP_NOTIFICATION_MESSAGE")
	overrideStringAny(&cfg.StartupRepairNotificationMessage, "OPENCLAW_AGENT_STARTUP_REPAIR_NOTIFICATION_MESSAGE")
	overrideStringAny(&cfg.OpenClawConfigPath, "OPENCLAW_AGENT_OPENCLAW_CONFIG_PATH")
	overrideStringAny(&cfg.OpenClawWorkspacePath, "OPENCLAW_AGENT_OPENCLAW_WORKSPACE_PATH")
	overrideStringAny(&cfg.OpenClawSkillsPath, "OPENCLAW_AGENT_OPENCLAW_SKILLS_PATH")
	overrideStringAny(&cfg.OpenClawBuiltinSkillsPath, "OPENCLAW_AGENT_OPENCLAW_BUILTIN_SKILLS_PATH")
	overrideStringAny(&cfg.OpenClawHealthURL, "OPENCLAW_AGENT_OPENCLAW_HEALTH_URL")
	overrideStringAny(&cfg.OpenClawStartupHealthTimeoutRaw, "OPENCLAW_AGENT_OPENCLAW_STARTUP_HEALTH_TIMEOUT")
	overrideStringAny(&cfg.OpenClawDefaultsDir, "OPENCLAW_AGENT_OPENCLAW_DEFAULTS_DIR")
	overrideStringAny(&cfg.AutostartDefaultsDir, "OPENCLAW_AGENT_AUTOSTART_DEFAULTS_DIR")
	overrideStringAny(&cfg.AutostartTargetDir, "OPENCLAW_AGENT_AUTOSTART_TARGET_DIR")
	overrideStringAny(&cfg.OpenClawExtensionsDir, "OPENCLAW_AGENT_OPENCLAW_EXTENSIONS_DIR")
	overrideStringAny(&cfg.OpenClawBundledExtensionsDir, "OPENCLAW_AGENT_OPENCLAW_BUNDLED_EXTENSIONS_DIR")
	overrideStringAny(&cfg.InstalledPluginPathPrefix, "OPENCLAW_AGENT_INSTALLED_PLUGIN_PATH_PREFIX")
	overrideStringAny(&cfg.DropUserName, "OPENCLAW_AGENT_DROP_USER_NAME")
	overrideBoolAny(&cfg.BrowserAutoLaunchEnabled, "OPENCLAW_AGENT_BROWSER_AUTO_LAUNCH_ENABLED")
	overrideStringAny(&cfg.BrowserExecutable, "OPENCLAW_AGENT_BROWSER_EXECUTABLE")
	overrideStringAny(&cfg.BrowserURL, "OPENCLAW_AGENT_BROWSER_URL")
	overrideStringAny(&cfg.WaylandSocketPath, "OPENCLAW_AGENT_WAYLAND_SOCKET_PATH")
	overrideStringAny(&cfg.BrowserLaunchWaylandTimeoutRaw, "OPENCLAW_AGENT_BROWSER_LAUNCH_WAYLAND_TIMEOUT")
	overrideStringAny(&cfg.BrowserLaunchExtraDelayRaw, "OPENCLAW_AGENT_BROWSER_LAUNCH_EXTRA_DELAY")
	overrideStringAny(&cfg.HeartbeatIntervalRaw, "OPENCLAW_AGENT_HEARTBEAT_INTERVAL")
	overrideStringAny(&cfg.StateReportIntervalRaw, "OPENCLAW_AGENT_STATE_REPORT_INTERVAL")
	overrideStringAny(&cfg.CommandPollIntervalRaw, "OPENCLAW_AGENT_COMMAND_POLL_INTERVAL")
	overrideStringAny(&cfg.CommandPollBackoffMaxRaw, "OPENCLAW_AGENT_COMMAND_POLL_BACKOFF_MAX")
	overrideStringAny(&cfg.RegisterRetryIntervalRaw, "OPENCLAW_AGENT_REGISTER_RETRY_INTERVAL")
	overrideStringAny(&cfg.ProcessStopTimeoutRaw, "OPENCLAW_AGENT_PROCESS_STOP_TIMEOUT")
	overrideStringAny(&cfg.SkillIncrementalRaw, "OPENCLAW_AGENT_SKILL_INCREMENTAL_INTERVAL")
	overrideStringAny(&cfg.SkillFullSyncRaw, "OPENCLAW_AGENT_SKILL_FULL_SYNC_INTERVAL")

	if raw := envFirst("OPENCLAW_AGENT_OPENCLAW_COMMAND"); raw != "" {
		cfg.OpenClawCommand = strings.Fields(raw)
	}
	if raw := envFirst("OPENCLAW_AGENT_OPENCLAW_DOCTOR_COMMAND"); raw != "" {
		cfg.OpenClawDoctorCommand = strings.Fields(raw)
	}
	overrideStringAny(&cfg.OpenClawDoctorPolicy, "OPENCLAW_AGENT_OPENCLAW_DOCTOR_POLICY")
	if raw := envFirst("OPENCLAW_AGENT_MAX_AUTO_RESTART"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OPENCLAW_AGENT_MAX_AUTO_RESTART: %w", err)
		}
		cfg.MaxAutoRestart = n
	}
	if raw := envFirst("OPENCLAW_AGENT_DISK_LIMIT_BYTES", "CLAWMANAGER_AGENT_DISK_LIMIT_BYTES"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse disk limit bytes: %w", err)
		}
		cfg.DiskLimitBytes = n
	}

	var err error
	if cfg.HeartbeatInterval, err = time.ParseDuration(cfg.HeartbeatIntervalRaw); err != nil {
		return Config{}, fmt.Errorf("parse heartbeat_interval: %w", err)
	}
	if cfg.StateReportInterval, err = time.ParseDuration(cfg.StateReportIntervalRaw); err != nil {
		return Config{}, fmt.Errorf("parse state_report_interval: %w", err)
	}
	if cfg.CommandPollInterval, err = time.ParseDuration(cfg.CommandPollIntervalRaw); err != nil {
		return Config{}, fmt.Errorf("parse command_poll_interval: %w", err)
	}
	if cfg.CommandPollBackoffMax, err = time.ParseDuration(cfg.CommandPollBackoffMaxRaw); err != nil {
		return Config{}, fmt.Errorf("parse command_poll_backoff_max: %w", err)
	}
	if cfg.RegisterRetryInterval, err = time.ParseDuration(cfg.RegisterRetryIntervalRaw); err != nil {
		return Config{}, fmt.Errorf("parse register_retry_interval: %w", err)
	}
	if cfg.ProcessStopTimeout, err = time.ParseDuration(cfg.ProcessStopTimeoutRaw); err != nil {
		return Config{}, fmt.Errorf("parse process_stop_timeout: %w", err)
	}
	if cfg.SkillIncrementalInterval, err = time.ParseDuration(cfg.SkillIncrementalRaw); err != nil {
		return Config{}, fmt.Errorf("parse skill_incremental_interval: %w", err)
	}
	if cfg.SkillFullSyncInterval, err = time.ParseDuration(cfg.SkillFullSyncRaw); err != nil {
		return Config{}, fmt.Errorf("parse skill_full_sync_interval: %w", err)
	}
	if cfg.BrowserLaunchWaylandTimeout, err = time.ParseDuration(cfg.BrowserLaunchWaylandTimeoutRaw); err != nil {
		return Config{}, fmt.Errorf("parse browser_launch_wayland_timeout: %w", err)
	}
	if cfg.BrowserLaunchExtraDelay, err = time.ParseDuration(cfg.BrowserLaunchExtraDelayRaw); err != nil {
		return Config{}, fmt.Errorf("parse browser_launch_extra_delay: %w", err)
	}
	if cfg.OpenClawStartupHealthTimeout, err = time.ParseDuration(cfg.OpenClawStartupHealthTimeoutRaw); err != nil {
		return Config{}, fmt.Errorf("parse openclaw_startup_health_timeout: %w", err)
	}

	if cfg.Enabled {
		if cfg.InstanceID == "" {
			return Config{}, errors.New("instance_id is required")
		}
		if cfg.BootstrapToken == "" {
			return Config{}, errors.New("bootstrap_token is required")
		}
		if cfg.ControlPlaneBaseURL == "" {
			return Config{}, errors.New("control_plane_base_url is required")
		}
	}
	if len(cfg.OpenClawCommand) == 0 {
		return Config{}, errors.New("openclaw_command is required")
	}

	cfg.RuntimeType = strings.TrimSpace(cfg.RuntimeType)
	if cfg.RuntimeType == "" {
		cfg.RuntimeType = "openclaw"
	}
	cfg.RuntimeName = strings.TrimSpace(cfg.RuntimeName)
	if cfg.RuntimeName == "" {
		cfg.RuntimeName = cfg.RuntimeType
	}
	cfg.DesktopBase = strings.TrimSpace(cfg.DesktopBase)
	if cfg.DesktopBase == "" {
		cfg.DesktopBase = "none"
	}
	cfg.AgentDataDir = cleanOptionalPath(cfg.AgentDataDir)
	cfg.DiskUsagePath = cleanOptionalPath(cfg.DiskUsagePath)
	cfg.OpenClawConfigPath = cleanOptionalPath(cfg.OpenClawConfigPath)
	cfg.OpenClawWorkspacePath = cleanOptionalPath(cfg.OpenClawWorkspacePath)
	cfg.OpenClawSkillsPath = cleanOptionalPath(cfg.OpenClawSkillsPath)
	cfg.OpenClawBuiltinSkillsPath = cleanOptionalPath(cfg.OpenClawBuiltinSkillsPath)
	cfg.OpenClawDefaultsDir = cleanOptionalPath(cfg.OpenClawDefaultsDir)
	cfg.AutostartDefaultsDir = cleanOptionalPath(cfg.AutostartDefaultsDir)
	cfg.AutostartTargetDir = cleanOptionalPath(cfg.AutostartTargetDir)
	cfg.OpenClawExtensionsDir = cleanOptionalPath(cfg.OpenClawExtensionsDir)
	cfg.OpenClawBundledExtensionsDir = cleanOptionalPath(cfg.OpenClawBundledExtensionsDir)
	return cfg, nil
}

func cleanOptionalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func overrideString(target *string, envKey string) {
	if value := os.Getenv(envKey); value != "" {
		*target = value
	}
}

func overrideStringAny(target *string, envKeys ...string) {
	if value := envFirst(envKeys...); value != "" {
		*target = value
	}
}

func overrideBoolAny(target *bool, envKeys ...string) {
	value := envFirst(envKeys...)
	if value == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		*target = true
	case "0", "false", "no", "off":
		*target = false
	}
}

func overrideIntAny(target *int, envKeys ...string) {
	value := envFirst(envKeys...)
	if value == "" {
		return
	}
	parsed, err := strconv.Atoi(value)
	if err == nil {
		*target = parsed
	}
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
