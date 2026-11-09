package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	teamConfigJSONEnv = "CLAWMANAGER_TEAM_CONFIG_JSON"
	teamConfigPathEnv = "CLAWMANAGER_TEAM_CONFIG_PATH"
	teamSharedDirEnv  = "CLAWMANAGER_TEAM_SHARED_DIR"
	teamUmaskEnv      = "CLAWMANAGER_TEAM_UMASK"
)

func ApplyRequestEnvironment(env []string, req CreateGatewayRequest) []string {
	env = applyEnvironmentMap(env, req.Env)
	env = applyEnvironmentMap(env, req.Environment)
	return env
}

func ApplyLiteTeamConfigEnvironment(env []string, req CreateGatewayRequest, workspacePath string) []string {
	configPath, sharedDir, ok := LiteTeamEnvironment(req, workspacePath)
	if !ok {
		return env
	}
	if configPath != "" {
		env = setEnv(env, teamConfigPathEnv, configPath)
	}
	if sharedDir != "" {
		env = setEnv(env, teamSharedDirEnv, sharedDir)
	}
	return env
}

func WriteLiteTeamConfigJSON(req CreateGatewayRequest, workspacePath string) error {
	raw, ok := requestEnvValue(req, teamConfigJSONEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	configPath, sharedDir, ok := LiteTeamEnvironment(req, workspacePath)
	if !ok {
		return nil
	}
	if configPath == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("parse %s: %w", teamConfigJSONEnv, err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lite team config: %w", err)
	}
	data = append(data, '\n')

	filePath := filepath.Clean(filepath.FromSlash(configPath))
	sharedRoot := filepath.Clean(filepath.FromSlash(sharedDir))
	if !pathWithin(sharedRoot, filePath) {
		return fmt.Errorf("lite Team config escaped shared workspace: %s", filePath)
	}
	root, err := openLiteTeamRoot("", req, workspacePath, sharedRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(filePath), 0750); err != nil {
		return fmt.Errorf("create lite team config dir: %w", err)
	}
	if err := root.WriteFile(filePath, data, 0664); err != nil {
		return fmt.Errorf("write lite team config: %w", err)
	}
	sharedGID := liteTeamSharedGID(req)
	if err := root.Chgrp(filepath.Dir(filePath), sharedGID); err != nil {
		return err
	}
	if err := root.Chgrp(filePath, sharedGID); err != nil {
		return err
	}
	return nil
}

func LiteTeamEnvironment(req CreateGatewayRequest, workspacePath string) (string, string, bool) {
	configJSON, hasConfigJSON := requestEnvValue(req, teamConfigJSONEnv)
	teamEnabled, hasTeamEnabled := requestEnvValue(req, "CLAWMANAGER_TEAM_ENABLED")
	if (!hasConfigJSON || strings.TrimSpace(configJSON) == "") && (!hasTeamEnabled || !truthyTeamEnv(teamEnabled)) {
		return "", "", false
	}
	configPath, _ := requestEnvValue(req, teamConfigPathEnv)
	configPath = strings.TrimSpace(configPath)
	sharedDir, _ := requestEnvValue(req, teamSharedDirEnv)
	sharedDir = strings.TrimSpace(sharedDir)
	if sharedDir == "" || isDefaultTeamSharedDir(sharedDir) {
		sharedDir = envPathJoin(workspacePath, "team")
	}

	switch {
	case configPath != "":
		if sharedDir == "" || isDefaultTeamSharedDir(sharedDir) {
			sharedDir = envPathDir(configPath)
		}
	case sharedDir != "":
		if hasConfigJSON && strings.TrimSpace(configJSON) != "" {
			configPath = envPathJoin(sharedDir, "team.json")
		}
	default:
		sharedDir = envPathJoin(workspacePath, "team")
		if hasConfigJSON && strings.TrimSpace(configJSON) != "" {
			configPath = envPathJoin(sharedDir, "team.json")
		}
	}
	if isDefaultTeamConfigPath(configPath) {
		configPath = envPathJoin(sharedDir, "team.json")
	}
	return configPath, sharedDir, true
}

func LiteTeamGatewayCommand(runtimeType string, command, env []string) []string {
	if len(command) == 0 || !truthyTeamEnv(envValue(env, "CLAWMANAGER_TEAM_ENABLED")) {
		return command
	}
	umask := strings.TrimSpace(envValue(env, teamUmaskEnv))
	if !validTeamUmask(umask) {
		umask = "0002"
	}
	wrapped := []string{"/bin/sh", "-c", `umask "$1"; shift; exec "$@"`, "clawmanager-team-gateway", umask}
	return append(wrapped, command...)
}

func applyEnvironmentMap(env []string, values map[string]string) []string {
	if len(values) == 0 {
		return env
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "" && !strings.Contains(key, "=") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = setEnv(env, key, values[key])
	}
	return env
}

func requestEnvValue(req CreateGatewayRequest, keys ...string) (string, bool) {
	_, value, ok := RequestEnvEntry(req, keys...)
	return value, ok
}

func RequestEnvValue(req CreateGatewayRequest, keys ...string) (string, bool) {
	_, value, ok := RequestEnvEntry(req, keys...)
	return value, ok
}

func RequestEnvEntry(req CreateGatewayRequest, keys ...string) (string, string, bool) {
	for _, key := range keys {
		if req.Environment != nil {
			if value, ok := req.Environment[key]; ok {
				return key, value, true
			}
		}
		if req.Env != nil {
			if value, ok := req.Env[key]; ok {
				return key, value, true
			}
		}
	}
	return "", "", false
}

func envPathJoin(base string, elems ...string) string {
	if usesWindowsSeparators(base) {
		parts := append([]string{base}, elems...)
		return filepath.Join(parts...)
	}
	parts := append([]string{base}, elems...)
	return path.Join(parts...)
}

func envPathDir(value string) string {
	if usesWindowsSeparators(value) {
		return filepath.Dir(value)
	}
	return path.Dir(value)
}

func usesWindowsSeparators(value string) bool {
	return strings.Contains(value, `\`) || filepath.VolumeName(value) != ""
}

func isDefaultTeamSharedDir(value string) bool {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(value))) == "/team"
}

func isDefaultTeamConfigPath(value string) bool {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	return normalized == "/etc/clawmanager/team/team.json" || normalized == "/team/team.json"
}

func validTeamUmask(value string) bool {
	if len(value) != 3 && len(value) != 4 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for idx := len(env) - 1; idx >= 0; idx-- {
		if strings.HasPrefix(env[idx], prefix) {
			return strings.TrimPrefix(env[idx], prefix)
		}
	}
	return ""
}

func liteTeamSharedGID(req CreateGatewayRequest) int {
	if raw, ok := requestEnvValue(req, "CLAWMANAGER_TEAM_SHARED_GID"); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return req.GID
}

func truthyTeamEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func SetEnvironmentValue(env []string, key, value string) []string  { return setEnv(env, key, value) }
func RemoveEnvironmentValues(env []string, keys ...string) []string { return unsetEnv(env, keys...) }
func EnvironmentValue(env []string, key string) string              { return envValue(env, key) }

// AppendImageEnvironment admits only static keys chosen by the runtime adapter.
func AppendImageEnvironment(base []string, keys ...string) []string {
	env := append([]string(nil), base...)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = setEnv(env, key, value)
		}
	}
	return env
}

func RuntimeGatewayCommand(cfg Config, command, env []string) []string {
	if profile, ok := cfg.Runtime.(interface {
		GatewayProcessCommand([]string, []string) []string
	}); ok {
		return profile.GatewayProcessCommand(command, env)
	}
	return command
}
