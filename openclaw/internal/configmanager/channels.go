package configmanager

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const pluginManifestName = "openclaw.plugin.json"
const cliStartupMetadataName = "cli-startup-metadata.json"
const redisTeamChannelID = "redis-team"

var envManagedChannelPlugins = map[string][]string{
	"dingtalk":           {"dingtalk-connector"},
	"dingtalk-connector": {"dingtalk-connector"},
	"wecom":              {"wecom-openclaw-plugin"},
}

// channelOverrides captures the inputs needed to reconcile the `channels`
// subtree and to rewrite installed plugin paths, replacing the inline
// Node.js block that used to live in scripts/99-openclaw-sync.
type channelOverrides struct {
	RawJSON                   string
	HasRawJSON                bool
	BundledExtensionsDir      string
	UserExtensionsDir         string
	PluginRegistryPath        string
	DefaultsDir               string
	ActiveConfigDir           string
	InstalledPluginPathPrefix string
}

func readChannelOverridesFromEnv(cfg appconfig.Config) channelOverrides {
	raw, has := os.LookupEnv("CLAWMANAGER_OPENCLAW_CHANNELS_JSON")
	activeConfigDir := filepath.Dir(cfg.OpenClawConfigPath)
	return channelOverrides{
		RawJSON:                   raw,
		HasRawJSON:                has,
		BundledExtensionsDir:      cfg.OpenClawBundledExtensionsDir,
		UserExtensionsDir:         cfg.OpenClawExtensionsDir,
		PluginRegistryPath:        filepath.Join(activeConfigDir, "plugins", "installs.json"),
		DefaultsDir:               cfg.OpenClawDefaultsDir,
		ActiveConfigDir:           activeConfigDir,
		InstalledPluginPathPrefix: cfg.InstalledPluginPathPrefix,
	}
}

// applyChannelOverrides mutates cfg to:
//   - rewrite plugins.installs[*].installPath prefixes so installs seeded
//     under /defaults/.openclaw/extensions/* point at the user extensions
//     directory on /config;
//   - sanitize the existing cfg.channels by dropping entries whose id is
//     not advertised by OpenClaw startup metadata or any bundled/user-installed plugin;
//   - merge any channels supplied via CLAWMANAGER_OPENCLAW_CHANNELS_JSON
//     after applying the same sanitization.
func applyChannelOverrides(cfg map[string]any, opts channelOverrides) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	envChannels, err := parseChannelsEnvJSON(opts.RawJSON, opts.HasRawJSON)
	if err != nil {
		return err
	}

	rewriteInstalledPluginPaths(cfg, opts.InstalledPluginPathPrefix, opts.UserExtensionsDir)

	supported := map[string]struct{}{}
	collectSupportedChannelIdsFromStartupMetadata(opts.BundledExtensionsDir, supported)
	collectSupportedChannelIds(opts.BundledExtensionsDir, supported)
	collectSupportedChannelIds(opts.UserExtensionsDir, supported)
	collectSupportedChannelIdsFromNPMProjects(opts.ActiveConfigDir, supported)
	collectSupportedChannelIdsFromRegistry(opts.PluginRegistryPath, opts.DefaultsDir, opts.ActiveConfigDir, supported)

	existing := ensureObject(cfg, "channels")
	sanitized := sanitizeChannels(existing, supported, "existing config")

	fromEnv := sanitizeChannels(envChannels, supported, "CLAWMANAGER_OPENCLAW_CHANNELS_JSON")
	for id, value := range fromEnv {
		sanitized[id] = value
	}
	reconcileRedisTeamChannel(sanitized, supported)
	if teamEnabledFromEnv() && os.Getenv("CLAWMANAGER_OPENCLAW_VERSION") == "2026.10.2" {
		materializeRedisTeamEnvDefaults(sanitized)
	}
	reconcileRedisTeamPluginEntry(cfg, supported)
	// Native config validation discovers disabled entries too. Selecting the
	// image-owned managed package is independent of enabling a Team channel.
	if os.Getenv("CLAWMANAGER_OPENCLAW_VERSION") == "2026.10.2" && managedTeamBundleEligible(opts.UserExtensionsDir) {
		if _, ok := supported[redisTeamChannelID]; ok {
			selectManagedBundledTeamPath(cfg, filepath.Join(opts.BundledExtensionsDir, redisTeamChannelID))
		}
	}
	reconcileEnvManagedChannelPlugins(cfg, fromEnv)
	cfg["channels"] = sanitized
	return nil
}

func managedTeamBundleEligible(extensions string) bool {
	data, err := os.ReadFile(filepath.Join(extensions, redisTeamChannelID, "package.json"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	return pkg.Name == "@clawmanager/openclaw-redis-team" || pkg.Name == "@clawmanager/redis-team"
}

// Use the native bundled selector, which retains bundled provenance while
// outranking a legacy per-instance install of the same managed Team package.
func selectManagedBundledTeamPath(cfg map[string]any, root string) {
	plugins, ok := cfg["plugins"].(map[string]any)
	if !ok {
		plugins = map[string]any{}
		cfg["plugins"] = plugins
	}
	load, ok := plugins["load"].(map[string]any)
	if !ok {
		load = map[string]any{}
		plugins["load"] = load
	}
	paths := []any{}
	switch previous := load["paths"].(type) {
	case []any:
		paths = previous
	case []string:
		for _, p := range previous {
			paths = append(paths, p)
		}
	}
	root = filepath.ToSlash(root)
	if !slices.ContainsFunc(paths, func(p any) bool { s, ok := p.(string); return ok && s == root }) {
		paths = append(paths, root)
	}
	load["paths"] = paths
}

func parseChannelsEnvJSON(raw string, present bool) (map[string]any, error) {
	if !present {
		return map[string]any{}, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("parse CLAWMANAGER_OPENCLAW_CHANNELS_JSON: %w", err)
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CLAWMANAGER_OPENCLAW_CHANNELS_JSON must be a JSON object")
	}
	return obj, nil
}

func rewriteInstalledPluginPaths(cfg map[string]any, prefix, userExtensionsDir string) {
	if prefix == "" || userExtensionsDir == "" {
		return
	}
	plugins, ok := cfg["plugins"].(map[string]any)
	if !ok {
		return
	}
	rewritePluginPathStrings(plugins, prefix, userExtensionsDir)
}

func collectSupportedChannelIdsFromStartupMetadata(bundledExtensionsDir string, out map[string]struct{}) {
	if bundledExtensionsDir == "" {
		return
	}
	metadataPath := filepath.Join(filepath.Dir(bundledExtensionsDir), cliStartupMetadataName)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("configmanager: read startup metadata %s: %v", metadataPath, err)
		}
		return
	}
	var metadata struct {
		ChannelOptions []string `json:"channelOptions"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		log.Printf("configmanager: parse startup metadata %s: %v", metadataPath, err)
		return
	}
	for _, id := range metadata.ChannelOptions {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
}

func reconcileRedisTeamChannel(channels map[string]any, supported map[string]struct{}) {
	if !teamEnabledFromEnv() {
		delete(channels, redisTeamChannelID)
		return
	}
	if _, ok := supported[redisTeamChannelID]; !ok {
		return
	}
	if _, ok := channels[redisTeamChannelID]; ok {
		return
	}
	channels[redisTeamChannelID] = map[string]any{
		"accounts": map[string]any{
			"default": map[string]any{
				"fromEnv": true,
			},
		},
	}
}

// 10.2 inspects channel configuration before loading its env-aware adapter.
// Publish the same effective defaults as readChannelConfig, preserving explicit
// account values and fromEnv=false accounts. This does not grant extra tools.
func materializeRedisTeamEnvDefaults(channels map[string]any) {
	channel, ok := channels[redisTeamChannelID].(map[string]any)
	if !ok {
		return
	}
	accounts, ok := channel["accounts"].(map[string]any)
	if !ok {
		return
	}
	for _, raw := range accounts {
		account, ok := raw.(map[string]any)
		if !ok || account["fromEnv"] == false {
			continue
		}
		if _, exists := account["enabled"]; !exists {
			account["enabled"] = true // caller already checked managed Team enablement
		}
		for configKey, envKey := range map[string]string{
			"redisUrl": "CLAWMANAGER_TEAM_REDIS_URL", "teamId": "CLAWMANAGER_TEAM_ID",
			"memberId": "CLAWMANAGER_TEAM_MEMBER_ID", "role": "CLAWMANAGER_TEAM_ROLE",
			"sharedDir": "CLAWMANAGER_TEAM_SHARED_DIR", "managerUrl": "CLAWMANAGER_TEAM_MANAGER_URL",
			"consumerGroup": "CLAWMANAGER_TEAM_CONSUMER_GROUP", "inboxKey": "CLAWMANAGER_TEAM_INBOX_KEY",
			"eventsKey": "CLAWMANAGER_TEAM_EVENTS_KEY", "presenceKey": "CLAWMANAGER_TEAM_PRESENCE_KEY",
			"dlqKey": "CLAWMANAGER_TEAM_DLQ_KEY",
		} {
			current, _ := account[configKey].(string)
			if strings.TrimSpace(current) == "" {
				if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
					account[configKey] = value
				}
			}
		}
		if _, exists := account["autoRun"]; !exists {
			if value := strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_AUTORUN")); value != "" {
				account["autoRun"] = value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes") || strings.EqualFold(value, "on")
			}
		}
		if _, exists := account["embeddedTimeoutSeconds"]; !exists {
			if value, err := strconv.Atoi(os.Getenv("CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS")); err == nil && value > 0 {
				account["embeddedTimeoutSeconds"] = value
			}
		}
	}
}

func reconcileRedisTeamPluginEntry(cfg map[string]any, supported map[string]struct{}) {
	plugins := ensureObject(cfg, "plugins")
	entries := ensureObject(plugins, "entries")
	entry := ensureObject(entries, redisTeamChannelID)
	if !teamEnabledFromEnv() {
		entry["enabled"] = false
		return
	}
	if _, ok := supported[redisTeamChannelID]; ok {
		entry["enabled"] = true
	}
}

func reconcileEnvManagedChannelPlugins(cfg map[string]any, envChannels map[string]any) {
	plugins := ensureObject(cfg, "plugins")
	entries := ensureObject(plugins, "entries")
	enabledPlugins := map[string]struct{}{}
	for channelID := range envChannels {
		for _, pluginID := range envManagedChannelPlugins[channelID] {
			enabledPlugins[pluginID] = struct{}{}
		}
	}
	managedPlugins := map[string]struct{}{}
	for _, pluginIDs := range envManagedChannelPlugins {
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

func teamEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_TEAM_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func rewritePluginPathStrings(value any, prefix, userExtensionsDir string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = rewritePluginPathStrings(child, prefix, userExtensionsDir)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = rewritePluginPathStrings(child, prefix, userExtensionsDir)
		}
		return typed
	case string:
		if rewritten, ok := rewritePathPrefix(typed, prefix, userExtensionsDir); ok {
			return rewritten
		}
		return typed
	default:
		return typed
	}
}

func collectSupportedChannelIdsFromRegistry(registryPath, defaultsDir, activeConfigDir string, out map[string]struct{}) {
	if registryPath == "" {
		return
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("configmanager: read plugin registry %s: %v", registryPath, err)
		}
		return
	}
	var registry struct {
		Plugins []struct {
			ManifestPath string `json:"manifestPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		log.Printf("configmanager: parse plugin registry %s: %v", registryPath, err)
		return
	}
	for _, plugin := range registry.Plugins {
		manifestPath := strings.TrimSpace(plugin.ManifestPath)
		if manifestPath == "" {
			continue
		}
		collectSupportedChannelIdsFromManifestCandidates(
			manifestPathCandidates(manifestPath, defaultsDir, activeConfigDir),
			out,
		)
	}
}

func manifestPathCandidates(manifestPath, defaultsDir, activeConfigDir string) []string {
	candidates := []string{manifestPath}
	if rewritten, ok := rewritePathPrefix(manifestPath, defaultsDir, activeConfigDir); ok && rewritten != manifestPath {
		candidates = append(candidates, rewritten)
	}
	return candidates
}

func collectSupportedChannelIds(rootDir string, out map[string]struct{}) {
	if rootDir == "" {
		return
	}
	info, err := os.Stat(rootDir)
	if err != nil || !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		log.Printf("configmanager: read plugins dir %s: %v", rootDir, err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(rootDir, entry.Name(), pluginManifestName)
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}
		collectSupportedChannelIdsFromManifestCandidates([]string{manifestPath}, out)
	}
}

// collectSupportedChannelIdsFromNPMProjects discovers plugins installed by
// newer OpenClaw releases. Those releases place npm plugins under isolated
// projects and may not create plugins/installs.json, so registry-only
// discovery would incorrectly discard an otherwise valid managed channel.
func collectSupportedChannelIdsFromNPMProjects(activeConfigDir string, out map[string]struct{}) {
	if activeConfigDir == "" {
		return
	}
	projectsDir := filepath.Join(activeConfigDir, "npm", "projects")
	info, err := os.Stat(projectsDir)
	if err != nil || !info.IsDir() {
		return
	}
	if err := filepath.WalkDir(projectsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			log.Printf("configmanager: walk npm plugin path %s: %v", path, err)
			return nil
		}
		if entry.IsDir() || entry.Name() != pluginManifestName {
			return nil
		}
		collectSupportedChannelIdsFromManifestCandidates([]string{path}, out)
		return nil
	}); err != nil {
		log.Printf("configmanager: walk npm projects dir %s: %v", projectsDir, err)
	}
}

func collectSupportedChannelIdsFromManifestCandidates(manifestPaths []string, out map[string]struct{}) {
	for _, manifestPath := range manifestPaths {
		if collectSupportedChannelIdsFromManifest(manifestPath, out) {
			return
		}
	}
}

func collectSupportedChannelIdsFromManifest(manifestPath string, out map[string]struct{}) bool {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		log.Printf("configmanager: read %s: %v", manifestPath, err)
		return false
	}
	var manifest struct {
		Channels []string `json:"channels"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Printf("configmanager: parse %s: %v", manifestPath, err)
		return false
	}
	for _, id := range manifest.Channels {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return true
}

func sanitizeChannels(source map[string]any, supported map[string]struct{}, label string) map[string]any {
	sanitized := make(map[string]any, len(source))
	for id, value := range source {
		if _, ok := supported[id]; ok {
			sanitized[id] = value
			continue
		}
		log.Printf("configmanager: skipping unsupported channel %q from %s; no matching extension was found", id, label)
	}
	return sanitized
}
