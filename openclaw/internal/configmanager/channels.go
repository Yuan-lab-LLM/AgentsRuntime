package configmanager

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const pluginManifestName = "openclaw.plugin.json"

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
//     not advertised by any bundled or user-installed plugin;
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
	collectSupportedChannelIds(opts.BundledExtensionsDir, supported)
	collectSupportedChannelIds(opts.UserExtensionsDir, supported)
	collectSupportedChannelIdsFromRegistry(opts.PluginRegistryPath, opts.DefaultsDir, opts.ActiveConfigDir, supported)

	existing := ensureObject(cfg, "channels")
	sanitized := sanitizeChannels(existing, supported, "existing config")

	fromEnv := sanitizeChannels(envChannels, supported, "CLAWMANAGER_OPENCLAW_CHANNELS_JSON")
	for id, value := range fromEnv {
		sanitized[id] = value
	}
	cfg["channels"] = sanitized
	return nil
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
