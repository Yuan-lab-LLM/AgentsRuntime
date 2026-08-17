package openclaw

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/scheduledtasks"
)

const openClawTrustedProxyUserHeader = "x-forwarded-prefix"
const openClawTrustedProxyRequiredHeader = "x-forwarded-proto"
const openClawTrustedProxyDefaultPassword = "9fb3edf4bf38bb834227d41fe9cc1196"
const openClawAutoProviderName = "auto"
const openClawRedisTeamPluginID = "redis-team"
const openClawRedisTeamPluginDirEnv = "CLAWMANAGER_OPENCLAW_REDIS_TEAM_PLUGIN_DIR"
const openClawBrowserExecutablePath = "/usr/bin/chromium"
const openClawBrowserProxyEnv = "CLAWMANAGER_BROWSER_PROXY_URL"
const openClawManagedBrowserProfile = "openclaw"
const openClawManagedBrowserColor = "#FF4500"
const openClawChannelsEnv = "CLAWMANAGER_OPENCLAW_CHANNELS_JSON"

var openClawDefaultDeniedNodeCommands = []string{
	"camera.snap",
	"camera.clip",
	"screen.record",
	"contacts.add",
	"calendar.add",
	"reminders.add",
	"sms.send",
}

var openClawDefaultDisabledPlugins = []string{
	"bonjour",
	"acpx",
	"phone-control",
	"talk-voice",
	"device-pair",
	"dingtalk-connector",
	"wecom-openclaw-plugin",
	"redis-team",
}

var openClawEnvManagedChannelPlugins = map[string][]string{
	"dingtalk":              {"dingtalk-connector"},
	"dingtalk-connector":    {"dingtalk-connector"},
	"feishu":                {"feishu"},
	"lark":                  {"feishu"},
	"wecom":                 {"wecom-openclaw-plugin"},
	"wecom-openclaw-plugin": {"wecom-openclaw-plugin"},
}

func WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) error {
	if port <= 0 {
		return fmt.Errorf("invalid gateway port %d", port)
	}
	if port > 65533 {
		return fmt.Errorf("invalid gateway port %d: managed OpenClaw Lite port block exceeds 65535", port)
	}
	if err := gateway.WriteLiteTeamConfigJSON(req, workspacePath); err != nil {
		return err
	}
	resolvedCfg, err := configWithRequestLLMEnv(cfg, req)
	if err != nil {
		return err
	}
	cfg = resolvedCfg

	instancePaths, err := resolveOpenClawInstancePaths(req, workspacePath)
	if err != nil {
		return err
	}
	configPath := filepath.Join(instancePaths.Persistent, "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return fmt.Errorf("create openclaw config dir: %w", err)
	}
	if err := seedOpenClawPluginRuntime(req, instancePaths.Persistent); err != nil {
		return err
	}

	config := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse openclaw config: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read openclaw config: %w", err)
	}
	configureManagedOpenClawBrowser(config, req, port)
	mergeOpenClawLiteDefaults(config)
	reconcileOpenClawBrowserPlugin(config)
	if err := mergeOpenClawChannelsFromRequest(config, req); err != nil {
		return err
	}

	mergePlatformDefaults(config, port)
	agentDefaults := ensureObject(ensureObject(config, "agents"), "defaults")
	agentDefaults["workspace"] = filepath.ToSlash(instancePaths.OpenClawWorkspace)
	if teamEnabledFromRequest(req) {
		if err := configureOpenClawRedisTeam(config, req, workspacePath); err != nil {
			return err
		}
		if err := seedOpenClawRedisTeamPlugin(req, workspacePath); err != nil {
			return err
		}
	}

	gatewayConfig := ensureObject(config, "gateway")
	auth := ensureObject(gatewayConfig, "auth")
	basePath := "/api/v1/instances/" + strconv.Itoa(req.InstanceID) + "/proxy"
	if cfg.GatewayAuthMode == "token" {
		auth["mode"] = "token"
	} else {
		auth["mode"] = "trusted-proxy"
		delete(auth, "token")
		if strings.TrimSpace(configStringValue(auth["password"])) == "" {
			auth["password"] = openClawTrustedProxyDefaultPassword
		}
		trustedProxy := ensureObject(auth, "trustedProxy")
		trustedProxy["userHeader"] = openClawTrustedProxyUserHeader
		trustedProxy["requiredHeaders"] = []string{openClawTrustedProxyRequiredHeader}
		trustedProxy["allowUsers"] = []string{basePath}
	}

	controlUI := ensureObject(gatewayConfig, "controlUi")
	controlUI["basePath"] = basePath
	if cfg.GatewayAuthMode == "trusted-proxy" {
		controlUI["dangerouslyDisableDeviceAuth"] = true
	}
	origins := cfg.AllowedOrigins
	if len(origins) == 0 && cfg.PublicOrigin != "" {
		origins = []string{cfg.PublicOrigin}
	}
	if len(origins) > 0 {
		controlUI["allowedOrigins"] = appendUniqueStringArray(controlUI["allowedOrigins"], origins...)
	}
	if len(cfg.TrustedProxies) > 0 {
		gatewayConfig["trustedProxies"] = appendUniqueStringArray(gatewayConfig["trustedProxies"], cfg.TrustedProxies...)
	}
	mergeOpenClawLLMConfig(config, cfg)

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openclaw config: %w", err)
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write openclaw config: %w", err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("chmod openclaw config: %w", err)
	}
	if err := gateway.ChownWorkspace(filepath.Dir(configPath), req.UID, req.GID); err != nil {
		return fmt.Errorf("chown openclaw config dir: %w", err)
	}
	if err := gateway.ChownWorkspace(configPath, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown openclaw config: %w", err)
	}
	if teamEnabledFromRequest(req) {
		if err := gateway.PrepareLiteTeamSharedWorkspace(cfg.WorkspaceRoot, req, workspacePath); err != nil {
			return err
		}
	}
	openclawHome := filepath.Join(workspacePath, "home", ".openclaw")
	result := scheduledtasks.ApplyOpenClawFromEnv(openclawHome, func(key string) string {
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
		log.Printf("apply openclaw scheduled tasks failed (continuing): source_env=%s error=%s", result.SourceEnv, result.Error)
	}
	// Apply writes jobs.json as the agent user (often root). Gateway runs as
	// instance UID, so chown the whole cron tree — not only the directory inode.
	cronDir := filepath.Join(openclawHome, "cron")
	if _, err := os.Stat(cronDir); err == nil {
		if err := chownTree(cronDir, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown openclaw cron tree: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat openclaw cron dir: %w", err)
	}
	return nil
}

// configureManagedOpenClawBrowser supplies the safe Lite runtime defaults that
// are present in the image template without replacing an instance's explicit
// browser choices. The managed local openclaw profile always receives the CDP
// port allocated inside this gateway's 3-port block. Team workers can also be
// forced through ClawManager's managed egress proxy when that proxy is present.
func configureManagedOpenClawBrowser(config map[string]any, req gateway.CreateGatewayRequest, gatewayPort int) {
	browser := ensureObject(config, "browser")
	setDefaultObjectValue(browser, "enabled", true)
	setDefaultObjectValue(browser, "executablePath", openClawBrowserExecutablePath)
	setDefaultObjectValue(browser, "headless", true)
	setDefaultObjectValue(browser, "noSandbox", true)

	profiles := ensureObject(browser, "profiles")
	profile := ensureObject(profiles, openClawManagedBrowserProfile)
	profile["driver"] = "openclaw"
	profile["cdpPort"] = gatewayPort + 1
	setDefaultObjectValue(profile, "color", openClawManagedBrowserColor)

	proxyURL, managed := managedOpenClawBrowserProxy(req)
	if !managed {
		return
	}

	// Team workers must all have a functioning Browser. Chromium is forced
	// through ClawManager's DNS-pinning egress proxy, so OpenClaw may permit
	// the proxy's internal service address without exposing arbitrary pod or
	// cluster addresses to the page being reviewed.
	browser["enabled"] = true
	browser["executablePath"] = openClawBrowserExecutablePath
	browser["headless"] = true
	browser["noSandbox"] = true
	browser["extraArgs"] = managedOpenClawBrowserArgs(browser["extraArgs"], proxyURL)
	ssrfPolicy := ensureObject(browser, "ssrfPolicy")
	ssrfPolicy["dangerouslyAllowPrivateNetwork"] = true
	// Do not add a hostnameAllowlist here. In OpenClaw it is an exclusive
	// navigation allowlist, not an additive DNS exception. The version-locked
	// image patch recognizes only ClawManager's signature-derived interactive
	// Preview host while keeping ordinary destinations on the upstream path.
}

// reconcileOpenClawBrowserPlugin keeps the 2026.7.1 pluginized Browser tool
// aligned with the long-standing browser.enabled runtime setting. Without
// this, an upgraded instance can launch Chromium but silently lose the Browser
// tool because older managed defaults explicitly disabled the plugin entry.
func reconcileOpenClawBrowserPlugin(config map[string]any) {
	browser := ensureObject(config, "browser")
	enabled, ok := browser["enabled"].(bool)
	if !ok {
		return
	}
	plugins := ensureObject(config, "plugins")
	entries := ensureObject(plugins, "entries")
	entry := ensureObject(entries, "browser")
	entry["enabled"] = enabled
}

func managedOpenClawBrowserProxy(req gateway.CreateGatewayRequest) (string, bool) {
	if !teamEnabledFromRequest(req) {
		return "", false
	}
	raw, explicitlyManaged := requestEnvValue(req, openClawBrowserProxyEnv)
	if !explicitlyManaged {
		raw, _ = requestEnvValue(req, "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy")
	}
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "clawmanager-egress-proxy" && !strings.HasPrefix(host, "clawmanager-egress-proxy.") {
		return "", false
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), true
}

func managedOpenClawBrowserArgs(existing any, proxyURL string) []string {
	current := appendUniqueStringArray(existing)
	filtered := make([]string, 0, len(current)+4)
	for _, argument := range current {
		normalized := strings.ToLower(strings.TrimSpace(argument))
		if normalized == "--no-proxy-server" ||
			strings.HasPrefix(normalized, "--proxy-") {
			continue
		}
		filtered = append(filtered, argument)
	}
	return append(filtered,
		"--proxy-server="+proxyURL,
		"--proxy-bypass-list=<-loopback>",
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
	)
}

func setDefaultObjectValue(object map[string]any, key string, value any) {
	if _, exists := object[key]; exists {
		return
	}
	object[key] = value
}

// mergeOpenClawLiteDefaults supplies the instance defaults that Pro receives
// from /defaults/.openclaw/openclaw.json. Lite workspaces start with an empty
// config, so these defaults must be added when the instance is created.
//
// Values are only filled when absent so recreating an existing Lite instance
// does not discard explicit user choices. Environment-managed model, channel,
// Team, gateway port, and authentication settings are applied afterwards.
func mergeOpenClawLiteDefaults(config map[string]any) {
	models := ensureObject(config, "models")
	setDefaultObjectValue(models, "mode", "merge")

	agentDefaults := ensureObject(ensureObject(config, "agents"), "defaults")
	memorySearch := ensureObject(agentDefaults, "memorySearch")
	setDefaultObjectValue(memorySearch, "enabled", true)
	setDefaultObjectValue(memorySearch, "provider", "none")

	compaction := ensureObject(agentDefaults, "compaction")
	setDefaultObjectValue(compaction, "mode", "default")
	setDefaultObjectValue(compaction, "reserveTokens", 32768)
	setDefaultObjectValue(compaction, "reserveTokensFloor", 20000)
	setDefaultObjectValue(compaction, "keepRecentTokens", 30000)
	setDefaultObjectValue(compaction, "maxHistoryShare", 0.65)
	setDefaultObjectValue(compaction, "notifyUser", true)
	memoryFlush := ensureObject(compaction, "memoryFlush")
	setDefaultObjectValue(memoryFlush, "enabled", true)

	setDefaultObjectValue(agentDefaults, "maxConcurrent", 4)
	subagents := ensureObject(agentDefaults, "subagents")
	setDefaultObjectValue(subagents, "maxConcurrent", 8)

	tools := ensureObject(config, "tools")
	setDefaultObjectValue(tools, "profile", "full")

	commands := ensureObject(config, "commands")
	setDefaultObjectValue(commands, "native", "auto")
	setDefaultObjectValue(commands, "nativeSkills", "auto")
	setDefaultObjectValue(commands, "restart", true)
	setDefaultObjectValue(commands, "ownerDisplay", "raw")

	messages := ensureObject(config, "messages")
	groupChat := ensureObject(messages, "groupChat")
	setDefaultObjectValue(groupChat, "visibleReplies", "automatic")

	gatewayConfig := ensureObject(config, "gateway")
	setDefaultObjectValue(gatewayConfig, "mode", "local")
	tailscale := ensureObject(gatewayConfig, "tailscale")
	setDefaultObjectValue(tailscale, "mode", "off")
	setDefaultObjectValue(tailscale, "resetOnExit", false)
	nodes := ensureObject(gatewayConfig, "nodes")
	nodes["denyCommands"] = appendUniqueStringArray(nodes["denyCommands"], openClawDefaultDeniedNodeCommands...)

	entries := ensureObject(ensureObject(config, "plugins"), "entries")
	for _, pluginID := range openClawDefaultDisabledPlugins {
		entry := ensureObject(entries, pluginID)
		setDefaultObjectValue(entry, "enabled", false)
	}
	memoryCore := ensureObject(entries, "memory-core")
	dreaming := ensureObject(ensureObject(memoryCore, "config"), "dreaming")
	setDefaultObjectValue(dreaming, "enabled", true)
	setDefaultObjectValue(dreaming, "frequency", "0 3 * * *")
	setDefaultObjectValue(dreaming, "timezone", "Asia/Shanghai")
}

func mergeOpenClawChannelsFromRequest(config map[string]any, req gateway.CreateGatewayRequest) error {
	raw, ok := requestEnvValue(req, openClawChannelsEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("parse %s: %w", openClawChannelsEnv, err)
	}
	channelsPayload, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a JSON object", openClawChannelsEnv)
	}

	channels := ensureObject(config, "channels")
	for name, channel := range channelsPayload {
		channels[name] = channel
	}
	reconcileOpenClawChannelPlugins(config, channelsPayload)
	applyOpenClawChannelDefaults(config, channelsPayload)
	return nil
}

func applyOpenClawChannelDefaults(config map[string]any, channelsPayload map[string]any) {
	if _, ok := channelsPayload["dingtalk"]; !ok {
		if _, ok := channelsPayload["dingtalk-connector"]; !ok {
			return
		}
	}

	messages := ensureObject(config, "messages")
	groupChat := ensureObject(messages, "groupChat")
	if value, ok := groupChat["visibleReplies"].(string); ok && strings.TrimSpace(value) != "" {
		return
	}
	groupChat["visibleReplies"] = "automatic"
}

func reconcileOpenClawChannelPlugins(config map[string]any, channelsPayload map[string]any) {
	plugins := ensureObject(config, "plugins")
	entries := ensureObject(plugins, "entries")
	enabledPlugins := map[string]struct{}{}
	for channelID := range channelsPayload {
		for _, pluginID := range openClawEnvManagedChannelPlugins[channelID] {
			enabledPlugins[pluginID] = struct{}{}
		}
	}
	managedPlugins := map[string]struct{}{}
	for _, pluginIDs := range openClawEnvManagedChannelPlugins {
		for _, pluginID := range pluginIDs {
			managedPlugins[pluginID] = struct{}{}
		}
	}
	for pluginID := range managedPlugins {
		entry := ensureObject(entries, pluginID)
		_, enabled := enabledPlugins[pluginID]
		entry["enabled"] = enabled
	}
}

func mergePlatformDefaults(config map[string]any, port int) {
	gatewayConfig := ensureObject(config, "gateway")
	gatewayConfig["port"] = port

	cron := ensureObject(config, "cron")
	cron["enabled"] = true
	cron["maxConcurrentRuns"] = 2
	runLog := ensureObject(cron, "runLog")
	runLog["keepLines"] = 2000
	runLog["maxBytes"] = "2mb"
	cron["sessionRetention"] = "24h"

	update := ensureObject(config, "update")
	update["checkOnStart"] = false
	auto := ensureObject(update, "auto")
	auto["enabled"] = false

	hooks := ensureObject(config, "hooks")
	internal := ensureObject(hooks, "internal")
	internal["enabled"] = true
	entries := ensureObject(internal, "entries")
	sessionMemory := ensureObject(entries, "session-memory")
	sessionMemory["enabled"] = true
	sessionMemory["messages"] = 50

	session := ensureObject(config, "session")
	reset := ensureObject(session, "reset")
	reset["idleMinutes"] = 10080
	reset["mode"] = "idle"
	maintenance := ensureObject(session, "maintenance")
	maintenance["maxEntries"] = 2000
	maintenance["pruneAfter"] = "180d"
}

func configureOpenClawRedisTeam(config map[string]any, req gateway.CreateGatewayRequest, workspacePath string) error {
	plugins := ensureObject(config, "plugins")
	entries := ensureObject(plugins, "entries")
	entry := ensureObject(entries, openClawRedisTeamPluginID)
	entry["enabled"] = true

	channels := ensureObject(config, "channels")
	channel := ensureObject(channels, openClawRedisTeamPluginID)
	accounts := ensureObject(channel, "accounts")
	account := ensureObject(accounts, "default")
	account["fromEnv"] = true
	account["enabled"] = true

	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_REDIS_URL", "redisUrl")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_ID", "teamId")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_MEMBER_ID", "memberId")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_ROLE", "role")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_MANAGER_URL", "managerUrl")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_CONSUMER_GROUP", "consumerGroup")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_INBOX_KEY", "inboxKey")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_EVENTS_KEY", "eventsKey")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_PRESENCE_KEY", "presenceKey")
	setTeamStringAccountValue(account, req, "CLAWMANAGER_TEAM_DLQ_KEY", "dlqKey")
	setTeamBoolAccountValue(account, req, "CLAWMANAGER_TEAM_AUTORUN", "autoRun")
	setTeamIntAccountValue(account, req, "CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS", "embeddedTimeoutSeconds")

	if _, sharedDir, ok := gateway.LiteTeamEnvironment(req, workspacePath); ok && strings.TrimSpace(sharedDir) != "" {
		account["sharedDir"] = sharedDir
		sharedDirPath := filepath.FromSlash(sharedDir)
		if err := os.MkdirAll(sharedDirPath, 0o750); err != nil {
			return fmt.Errorf("create lite team shared dir: %w", err)
		}
	}
	return nil
}

func setTeamStringAccountValue(account map[string]any, req gateway.CreateGatewayRequest, envKey, configKey string) {
	value, ok := requestEnvValue(req, envKey)
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	account[configKey] = value
}

func setTeamBoolAccountValue(account map[string]any, req gateway.CreateGatewayRequest, envKey, configKey string) {
	value, ok := requestEnvValue(req, envKey)
	if !ok {
		return
	}
	account[configKey] = truthyOpenClawTeamEnv(value)
}

func setTeamIntAccountValue(account map[string]any, req gateway.CreateGatewayRequest, envKey, configKey string) {
	value, ok := requestEnvValue(req, envKey)
	if !ok {
		return
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return
	}
	account[configKey] = parsed
}

func teamEnabledFromRequest(req gateway.CreateGatewayRequest) bool {
	value, ok := requestEnvValue(req, "CLAWMANAGER_TEAM_ENABLED")
	return ok && truthyOpenClawTeamEnv(value)
}

func truthyOpenClawTeamEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func seedOpenClawRedisTeamPlugin(req gateway.CreateGatewayRequest, workspacePath string) error {
	source, ok, err := findOpenClawRedisTeamPluginSource()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("redis-team plugin source not found; checked %s and default OpenClaw extension locations", openClawRedisTeamPluginDirEnv)
	}
	extensionsDir := filepath.Join(workspacePath, "home", ".openclaw", "extensions")
	target := filepath.Join(extensionsDir, openClawRedisTeamPluginID)
	if _, err := os.Stat(filepath.Join(target, "openclaw.plugin.json")); err == nil {
		if err := chownTree(target, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown redis-team plugin: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat redis-team plugin target: %w", err)
	}
	if err := copyDir(source, target); err != nil {
		return fmt.Errorf("seed redis-team plugin: %w", err)
	}
	if err := chownTree(extensionsDir, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown redis-team plugin: %w", err)
	}
	return nil
}

func findOpenClawRedisTeamPluginSource() (string, bool, error) {
	candidates := []string{}
	if value := strings.TrimSpace(os.Getenv(openClawRedisTeamPluginDirEnv)); value != "" {
		candidates = append(candidates, value)
	}
	candidates = append(candidates,
		"/config/.openclaw/extensions/redis-team",
		"/defaults/.openclaw/extensions/redis-team",
	)
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		info, err := os.Stat(clean)
		if err == nil {
			if !info.IsDir() {
				return "", false, fmt.Errorf("redis-team plugin source is not a directory: %s", clean)
			}
			manifest := filepath.Join(clean, "openclaw.plugin.json")
			if _, err := os.Stat(manifest); err != nil {
				return "", false, fmt.Errorf("redis-team plugin source missing manifest %s: %w", manifest, err)
			}
			return clean, true, nil
		}
		if os.IsNotExist(err) {
			continue
		}
		return "", false, fmt.Errorf("stat redis-team plugin source %s: %w", clean, err)
	}
	return "", false, nil
}

func copyDir(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", source)
	}
	if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(source, entry.Name())
		dstPath := filepath.Join(target, entry.Name())
		entryInfo, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, dstPath); err != nil {
				return err
			}
			continue
		}
		if entryInfo.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		if err := copyRegularFile(srcPath, dstPath, entryInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(source, target string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return gateway.ChownWorkspace(path, uid, gid)
	})
}

func configWithRequestLLMEnv(cfg gateway.Config, req gateway.CreateGatewayRequest) (gateway.Config, error) {
	resolved := cfg
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"); ok && strings.TrimSpace(value) != "" {
		resolved.LLMBaseURL = strings.TrimSpace(value)
	}
	if value, ok := requestEnvValue(req, "CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"); ok {
		resolved.LLMAPIKey = value
		resolved.LLMAPIKeySet = true
	}
	if raw, ok := requestEnvValue(req, "CLAWMANAGER_LLM_MODEL", "OPENAI_MODEL"); ok && strings.TrimSpace(raw) != "" {
		modelIDs, err := parseLLMModelIDs(raw)
		if err != nil {
			return gateway.Config{}, err
		}
		resolved.LLMModelIDs = modelIDs
	}
	if raw, ok := requestEnvValue(req, "CLAWMANAGER_LLM_REASONING"); ok && strings.TrimSpace(raw) != "" {
		reasoning, err := parseLLMReasoning(raw)
		if err != nil {
			return gateway.Config{}, err
		}
		resolved.LLMReasoning = reasoning
	}
	if raw, ok := requestEnvValue(req, "CLAWMANAGER_LLM_REASONING_CONTROL"); ok && strings.TrimSpace(raw) != "" {
		controls, err := parseLLMReasoningControl(raw)
		if err != nil {
			return gateway.Config{}, err
		}
		resolved.LLMReasoningControl = controls
	}
	return resolved, nil
}

func requestEnvValue(req gateway.CreateGatewayRequest, keys ...string) (string, bool) {
	for _, key := range keys {
		if req.Environment != nil {
			if value, ok := req.Environment[key]; ok {
				return value, true
			}
		}
		if req.Env != nil {
			if value, ok := req.Env[key]; ok {
				return value, true
			}
		}
	}
	return "", false
}

func mergeOpenClawLLMConfig(config map[string]any, cfg gateway.Config) {
	if cfg.LLMBaseURL == "" && !cfg.LLMAPIKeySet && len(cfg.LLMModelIDs) == 0 {
		normalizeOpenClawProviderAuthContracts(config)
		return
	}

	models := ensureObject(config, "models")
	providers := ensureObject(models, "providers")
	provider := ensureObject(providers, openClawAutoProviderName)
	if cfg.LLMBaseURL != "" {
		provider["baseUrl"] = cfg.LLMBaseURL
	}
	if cfg.LLMAPIKeySet {
		provider["apiKey"] = cfg.LLMAPIKey
	}
	if strings.TrimSpace(configStringValue(provider["api"])) == "" {
		provider["api"] = "openai-completions"
	}
	if strings.TrimSpace(configStringValue(provider["auth"])) == "" && strings.TrimSpace(cfg.LLMAPIKey) != "" {
		provider["auth"] = "api-key"
	}
	if len(cfg.LLMModelIDs) > 0 {
		provider["models"] = buildOpenClawProviderModels(provider["models"], cfg.LLMModelIDs, cfg.LLMReasoning, cfg.LLMReasoningControl)

		agents := ensureObject(config, "agents")
		defaults := ensureObject(agents, "defaults")
		model := ensureObject(defaults, "model")
		primaryModel := qualifiedOpenClawModelID(openClawAutoProviderName, cfg.LLMModelIDs[0])
		model["primary"] = primaryModel
		defaults["models"] = buildOpenClawAgentModels(defaults["models"], openClawAutoProviderName, cfg.LLMModelIDs)
		// OpenClaw 2026.7.1 auto-discovers a built-in OpenAI image fallback when
		// imageModel is unset. ClawManager also exports OPENAI_* compatibility
		// aliases, so that fallback can look configured even though the user did
		// not enable it. Keep image calls on the managed provider while preserving
		// an explicit custom image model.
		if rawImageModel, exists := defaults["imageModel"]; !exists || rawImageModel == nil {
			defaults["imageModel"] = map[string]any{"primary": primaryModel}
		}
	}
	normalizeOpenClawProviderAuthContracts(config)
}

func normalizeOpenClawProviderAuthContracts(config map[string]any) {
	models, ok := config["models"].(map[string]any)
	if !ok {
		return
	}
	providers, ok := models["providers"].(map[string]any)
	if !ok {
		return
	}
	for _, raw := range providers {
		provider, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(configStringValue(provider["auth"])) != "" {
			continue
		}
		if strings.TrimSpace(configStringValue(provider["apiKey"])) == "" {
			continue
		}
		provider["auth"] = "api-key"
	}
}

func buildOpenClawProviderModels(existing any, modelIDs []string, reasoningByID map[string]bool, reasoningControlByID map[string]string) []any {
	byID := indexOpenClawModelsByID(existing)
	models := make([]any, 0, len(modelIDs))
	for _, id := range modelIDs {
		if current, ok := byID[id]; ok {
			cloned := cloneOpenClawMap(current)
			cloned["id"] = id
			if reasoning, managed := reasoningByID[id]; managed {
				cloned["reasoning"] = reasoning
			}
			applyOpenClawReasoningControlCompat(cloned, reasoningByID[id], reasoningControlByID[id])
			if strings.EqualFold(id, "auto") || strings.TrimSpace(configStringValue(cloned["name"])) == "" {
				cloned["name"] = displayOpenClawModelName(id)
			}
			models = append(models, cloned)
			continue
		}
		model := defaultOpenClawProviderModel(id, reasoningByID[id])
		applyOpenClawReasoningControlCompat(model, reasoningByID[id], reasoningControlByID[id])
		models = append(models, model)
	}
	return models
}

func applyOpenClawReasoningControlCompat(model map[string]any, enabled bool, control string) {
	if model == nil || control != "deepseek-thinking" {
		return
	}
	if !enabled {
		return
	}
	compat, _ := model["compat"].(map[string]any)
	if compat == nil {
		compat = map[string]any{}
		model["compat"] = compat
	}
	compat["supportsReasoningEffort"] = true
	compat["supportedReasoningEfforts"] = []any{"high", "max"}
	compat["reasoningEffortMap"] = map[string]any{
		"off": "none", "minimal": "high", "low": "high", "medium": "high",
		"high": "high", "xhigh": "max", "max": "max",
	}
}

func indexOpenClawModelsByID(existing any) map[string]map[string]any {
	items, ok := existing.([]any)
	if !ok {
		return map[string]map[string]any{}
	}
	index := make(map[string]map[string]any, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(configStringValue(model["id"]))
		if id != "" {
			index[id] = model
		}
	}
	return index
}

func buildOpenClawAgentModels(existing any, providerName string, modelIDs []string) map[string]any {
	current, _ := existing.(map[string]any)
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		key := qualifiedOpenClawModelID(providerName, id)
		if current != nil {
			if value, ok := current[key]; ok {
				models[key] = value
				continue
			}
		}
		models[key] = map[string]any{}
	}
	return models
}

func defaultOpenClawProviderModel(id string, reasoning bool) map[string]any {
	return map[string]any{
		"id":        id,
		"name":      displayOpenClawModelName(id),
		"reasoning": reasoning,
		"input": []any{
			"text",
		},
		"cost": map[string]any{
			"input":      0,
			"output":     0,
			"cacheRead":  0,
			"cacheWrite": 0,
		},
		"contextWindow": 1000000,
		"maxTokens":     65536,
	}
}

func qualifiedOpenClawModelID(providerName, id string) string {
	return providerName + "/" + id
}

func displayOpenClawModelName(id string) string {
	if strings.EqualFold(id, "auto") {
		return "Auto"
	}
	return id
}

func cloneOpenClawMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func configStringValue(value any) string {
	switch raw := value.(type) {
	case string:
		return raw
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func ensureObject(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}

func appendUniqueStringArray(existing any, values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	switch typed := existing.(type) {
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if ok && text != "" && !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	case []string:
		for _, text := range typed {
			if text != "" && !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	}
	for _, text := range values {
		if text != "" && !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	return out
}
