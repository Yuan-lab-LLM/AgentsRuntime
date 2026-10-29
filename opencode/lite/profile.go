package opencode

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type Profile struct {
	runtimeType string
}

func NewProfile(runtimeType string) Profile {
	return Profile{runtimeType: strings.ToLower(strings.TrimSpace(runtimeType))}
}

func (p Profile) Type() string {
	return p.runtimeType
}

func (p Profile) DisplayName() string {
	return "OpenCode"
}

func (p Profile) Defaults() gateway.RuntimeDefaults {
	return gateway.RuntimeDefaults{
		WorkspaceRoot:         "/workspaces",
		AgentDataDir:          "/var/lib/clawmanager-agent",
		GatewayPortStart:      20000,
		GatewayPortEnd:        20299,
		GatewayPortBlockSize:  1,
		GatewayCapacity:       100,
		GatewayAuthMode:       "token",
		GatewayStartupTimeout: 90 * time.Second,
	}
}

func (p Profile) GatewayCommand(string) []string {
	return []string{"start-opencode-web"}
}

func (p Profile) GatewayEnv(base []string, cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = gateway.ApplyRequestEnvironment(env, req)
	projectPath, projectRelativePath, fallbackReason := resolveOpenCodeProjectPath(req, workspacePath)
	if fallbackReason != "" {
		log.Printf("runtime-agent OpenCode default project fallback: instance_id=%d workspace_path=%q project_relative_path=%q reason=%s", req.InstanceID, workspacePath, req.ProjectRelativePath, fallbackReason)
	}
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_GATEWAY_GENERATION", strconv.Itoa(req.Generation))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_PROJECT_PATH", projectPath)
	env = setEnv(env, "CLAWMANAGER_DEFAULT_PROJECT_PATH", projectPath)
	if projectRelativePath == "" {
		env = unsetEnv(env, "CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH")
	} else {
		env = setEnv(env, "CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH", projectRelativePath)
	}
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	// ClawManager's workspace browser exposes workspacePath as its root. Keep
	// OpenCode's idea of the user's home aligned with that root as well, or a
	// project selected as "mcpclient" is resolved as workspacePath/home/mcpclient
	// even though uploads are stored at workspacePath/mcpclient.
	//
	// OpenCode's managed configuration and XDG state remain under the dedicated
	// home directory below so changing the browsing root does not move or expose
	// application state in the workspace browser.
	homePath := path.Join(workspacePath, "home")
	env = setEnv(env, "HOME", workspacePath)
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))

	opencodeHome := path.Join(homePath, ".opencode")
	configPath := path.Join(opencodeHome, "opencode.json")
	env = setEnv(env, "OPENCODE_CONFIG_DIR", opencodeHome)
	env = setEnv(env, "OPENCODE_CONFIG", configPath)
	env = setEnv(env, "XDG_CONFIG_HOME", path.Join(homePath, ".config"))
	env = setEnv(env, "XDG_CACHE_HOME", path.Join(homePath, ".cache"))
	env = setEnv(env, "XDG_DATA_HOME", path.Join(homePath, ".local", "share"))
	env = setEnv(env, "XDG_STATE_HOME", path.Join(homePath, ".local", "state"))
	// OpenCode 1.18.x's FFF backend refuses to search a directory that is also
	// HOME (and can return an empty project picker). A managed Lite workspace is
	// already isolated per instance, so use the ripgrep fallback. This keeps the
	// upload root and OpenCode's project picker root identical without enabling
	// expensive cross-home or filesystem-root indexing.
	env = setEnv(env, "OPENCODE_DISABLE_FFF", "1")
	// The sandbox hides cgroup files. Give the embedded Bun/JSC collector a
	// conservative RAM estimate so multi-project initialization collects before
	// the slot's memory limit. This tunes collection; Kubernetes enforces the cap.
	env = setEnv(env, "BUN_JSC_forceRAMSize", "536870912")

	username, password := resolveOpenCodeServerAuth(cfg, req)
	env = setEnv(env, "OPENCODE_SERVER_USERNAME", username)
	env = setEnv(env, "OPENCODE_SERVER_PASSWORD", password)

	env = unsetEnv(
		env,
		"RUNTIME_AGENT_CONTROL_TOKEN",
		"RUNTIME_AGENT_REPORT_TOKEN",
		"RUNTIME_AGENT_DATA_DIR",
		"RUNTIME_AGENT_PUBLIC_PORT",
		"RUNTIME_AGENT_LISTEN_ADDR",
	)
	return env
}

func resolveOpenCodeProjectPath(req gateway.CreateGatewayRequest, workspacePath string) (string, string, string) {
	workspacePath = cleanRuntimeWorkspacePath(workspacePath)
	rawPath, absolute, supplied := requestedOpenCodeProjectPath(req)

	var candidate string
	var relativePath string
	if !supplied {
		candidate = filepath.Join(workspacePath, "project")
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			return workspacePath, "", ""
		}
	} else if absolute {
		candidate = filepath.Clean(rawPath)
		relative, err := filepath.Rel(workspacePath, candidate)
		if err != nil || !pathWithinWorkspace(workspacePath, candidate) {
			return workspacePath, "", "absolute compatibility path is outside the instance workspace"
		}
		if relative == "." {
			return workspacePath, "", ""
		}
		relativePath = filepath.ToSlash(relative)
	} else {
		normalized, err := normalizeProjectRelativePath(rawPath)
		if err != nil {
			return workspacePath, "", err.Error()
		}
		relativePath = normalized
		candidate = filepath.Join(workspacePath, filepath.FromSlash(normalized))
		if !pathWithinWorkspace(workspacePath, candidate) {
			return workspacePath, "", "resolved project path is outside the instance workspace"
		}
	}

	workspaceRealPath, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return workspacePath, "", fmt.Sprintf("resolve workspace path: %v", err)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return workspacePath, "", fmt.Sprintf("project directory is unavailable: %v", err)
	}
	if !info.IsDir() {
		return workspacePath, "", "project path is not a directory"
	}
	projectRealPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return workspacePath, "", fmt.Sprintf("resolve project path: %v", err)
	}
	if !pathWithinWorkspace(workspaceRealPath, projectRealPath) {
		return workspacePath, "", "project symbolic link escapes the instance workspace"
	}
	return filepath.Clean(candidate), relativePath, ""
}

func cleanRuntimeWorkspacePath(value string) string {
	value = strings.TrimSpace(value)
	if filepath.VolumeName(value) == "" && strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return filepath.Clean(value)
}

func requestedOpenCodeProjectPath(req gateway.CreateGatewayRequest) (string, bool, bool) {
	if value := strings.TrimSpace(req.ProjectRelativePath); value != "" {
		return value, false, true
	}
	if value, ok := requestEnvValue(req, "CLAWMANAGER_DEFAULT_PROJECT_RELATIVE_PATH"); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), false, true
	}
	if value, ok := requestEnvValue(req, "CLAWMANAGER_DEFAULT_PROJECT_PATH"); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true, true
	}
	return "", false, false
}

func normalizeProjectRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("project relative path is empty")
	}
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", fmt.Errorf("project relative path contains unsupported characters")
	}
	if path.IsAbs(value) || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("project relative path must not be absolute")
	}
	normalized := path.Clean(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("project relative path escapes the instance workspace")
	}
	return normalized, nil
}

func pathWithinWorkspace(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == candidate {
		return true
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (p Profile) PrepareWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string) error {
	prepared, err := gateway.PrepareWorkspace(cfg.WorkspaceRoot, cfg.RuntimeType, req)
	if err != nil {
		return err
	}
	if prepared != workspacePath {
		return fmt.Errorf("%w: prepared %s want %s", gateway.ErrWorkspacePath, prepared, workspacePath)
	}
	if err := prepareOpenCodeProjectDirectory(req, workspacePath); err != nil {
		return err
	}
	root, err := workspaceio.Open(workspacePath)
	if err != nil {
		return err
	}
	defer root.Close()
	opencodeHome := filepath.Join(workspacePath, "home", ".opencode")
	if err := root.MkdirAll(opencodeHome, 0o750); err != nil {
		return fmt.Errorf("create opencode home: %w", err)
	}
	if err := root.Chown(opencodeHome, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown opencode home: %w", err)
	}
	return nil
}

func prepareOpenCodeProjectDirectory(req gateway.CreateGatewayRequest, workspacePath string) error {
	if strings.TrimSpace(req.ProjectRelativePath) == "" {
		return nil
	}
	relativePath, err := normalizeProjectRelativePath(req.ProjectRelativePath)
	if err != nil {
		return fmt.Errorf("prepare OpenCode default project: %w", err)
	}

	root, err := workspaceio.Open(workspacePath)
	if err != nil {
		return err
	}
	defer root.Close()
	current := filepath.Clean(workspacePath)
	for _, segment := range strings.Split(relativePath, "/") {
		current = filepath.Join(current, segment)
		if !pathWithinWorkspace(workspacePath, current) {
			return fmt.Errorf("prepare OpenCode default project: path escaped the instance workspace")
		}
		info, statErr := root.Lstat(current)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("prepare OpenCode default project: %s is a symbolic link", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("prepare OpenCode default project: %s is not a directory", current)
			}
		case os.IsNotExist(statErr):
			if mkdirErr := root.Mkdir(current, 0o750); mkdirErr != nil {
				return fmt.Errorf("prepare OpenCode default project: create %s: %w", current, mkdirErr)
			}
		default:
			return fmt.Errorf("prepare OpenCode default project: inspect %s: %w", current, statErr)
		}
	}
	if err := root.Chown(current, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown OpenCode default project: %w", err)
	}
	return nil
}

func (p Profile) WriteGatewayConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspacePath string, _ int) error {
	return WriteGatewayConfig(cfg, req, workspacePath)
}

func (p Profile) HealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return newHealthChecker(cfg)
}

func resolveOpenCodeServerAuth(cfg gateway.Config, req gateway.CreateGatewayRequest) (string, string) {
	username := "opencode"
	if value, ok := requestEnvValue(req, "OPENCODE_SERVER_USERNAME"); ok && strings.TrimSpace(value) != "" {
		username = strings.TrimSpace(value)
	}

	if cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile {
		password, _ := requestEnvValue(req, "CLAWMANAGER_INSTANCE_TOKEN")
		return username, strings.TrimSpace(password)
	}
	password := strings.TrimSpace(cfg.GatewayToken)
	if password == "" {
		if value, ok := requestEnvValue(
			req,
			"OPENCODE_SERVER_PASSWORD",
			"CLAWMANAGER_INSTANCE_ACCESS_TOKEN",
			"CLAWMANAGER_INSTANCE_TOKEN",
			"CLAWMANAGER_LLM_API_KEY",
			"OPENAI_API_KEY",
			"CLAWMANAGER_GATEWAY_TOKEN",
		); ok {
			password = strings.TrimSpace(value)
		}
	}
	return username, password
}

func requestEnvValue(req gateway.CreateGatewayRequest, keys ...string) (string, bool) {
	return gateway.RequestEnvValue(req, keys...)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for index, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func unsetEnv(env []string, keys ...string) []string {
	remove := map[string]bool{}
	for _, key := range keys {
		remove[key+"="] = true
	}
	filtered := env[:0]
	for _, item := range env {
		keep := true
		for prefix := range remove {
			if strings.HasPrefix(item, prefix) {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
