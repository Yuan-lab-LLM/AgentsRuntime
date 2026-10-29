package agent

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

func RuntimeAgentModeEnabled() bool {
	return os.Getenv("RUNTIME_AGENT_CONTROL_TOKEN") != "" || os.Getenv("RUNTIME_AGENT_REPORT_TOKEN") != ""
}

func LoadConfigFromEnv(profile gateway.RuntimeProfile) (Config, error) {
	if profile == nil {
		return Config{}, errors.New("runtime profile is required")
	}
	runtimeType := strings.ToLower(strings.TrimSpace(envOrDefault("CLAWMANAGER_RUNTIME_TYPE", profile.Type())))
	if runtimeType != profile.Type() {
		return Config{}, fmt.Errorf("runtime profile %q cannot run %q", profile.Type(), runtimeType)
	}
	defaults := profile.Defaults()

	namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if namespace == "" {
		namespace = readNamespaceFile()
	}
	if namespace == "" {
		namespace = "default"
	}

	publicPort, err := intEnv("RUNTIME_AGENT_PUBLIC_PORT", 19090)
	if err != nil {
		return Config{}, err
	}
	defaultListenAddr := "0.0.0.0:19090"
	if strings.TrimSpace(os.Getenv("RUNTIME_AGENT_ROLE")) == "pool" {
		defaultListenAddr = ":19090"
	}
	listenAddr := envOrDefault("RUNTIME_AGENT_LISTEN_ADDR", defaultListenAddr)
	podIP := strings.TrimSpace(os.Getenv("POD_IP"))
	if podIP == "" {
		podIP = firstNonLoopbackIP()
	}
	if podIP == "" {
		podIP = "0.0.0.0"
	}

	podName := strings.TrimSpace(os.Getenv("POD_NAME"))
	if podName == "" {
		podName, _ = os.Hostname()
	}
	if podName == "" {
		podName = runtimeType + "-runtime"
	}

	gatewayAuthMode := strings.ToLower(strings.TrimSpace(envFirst("RUNTIME_GATEWAY_AUTH_MODE", "OPENCLAW_GATEWAY_AUTH_MODE")))
	if gatewayAuthMode == "" {
		gatewayAuthMode = defaults.GatewayAuthMode
	}
	switch gatewayAuthMode {
	case "token", "trusted-proxy":
	default:
		return Config{}, fmt.Errorf("invalid OPENCLAW_GATEWAY_AUTH_MODE %q", gatewayAuthMode)
	}

	cfg := Config{
		RuntimeType:           runtimeType,
		OpenClawVersion:       strings.TrimSpace(os.Getenv("CLAWMANAGER_OPENCLAW_VERSION")),
		UpgradeID:             strings.TrimSpace(os.Getenv("CLAWMANAGER_RUNTIME_UPGRADE_ID")),
		Runtime:               profile,
		WorkspaceRoot:         cleanPath(envOrDefault("RUNTIME_WORKSPACE_ROOT", defaults.WorkspaceRoot)),
		ControlToken:          strings.TrimSpace(os.Getenv("RUNTIME_AGENT_CONTROL_TOKEN")),
		ReportToken:           strings.TrimSpace(os.Getenv("RUNTIME_AGENT_REPORT_TOKEN")),
		BackendURL:            strings.TrimRight(envOrDefault("CLAWMANAGER_BACKEND_URL", "http://clawmanager-gateway."+namespace+".svc.cluster.local:9001"), "/"),
		ListenAddr:            listenAddr,
		TLSCertFile:           strings.TrimSpace(os.Getenv("RUNTIME_AGENT_TLS_CERT_FILE")),
		TLSKeyFile:            strings.TrimSpace(os.Getenv("RUNTIME_AGENT_TLS_KEY_FILE")),
		PublicPort:            publicPort,
		ImageRef:              strings.TrimSpace(envOrDefault("CLAWMANAGER_RUNTIME_IMAGE_REF", runtimeType+":dev")),
		Namespace:             namespace,
		PodName:               podName,
		PodUID:                strings.TrimSpace(os.Getenv("POD_UID")),
		PodIP:                 podIP,
		NodeName:              strings.TrimSpace(os.Getenv("NODE_NAME")),
		DeploymentName:        deploymentName(podName, runtimeType),
		Capacity:              defaults.GatewayCapacity,
		GatewayAuthMode:       gatewayAuthMode,
		GatewayCommand:        commandFromEnv(profile, gatewayAuthMode),
		GatewayToken:          strings.TrimSpace(envFirst("OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")),
		AgentDataDir:          cleanPath(envOrDefault("RUNTIME_AGENT_DATA_DIR", defaults.AgentDataDir)),
		HeartbeatInterval:     2 * time.Second,
		MetricsInterval:       5 * time.Second,
		GatewayReportInterval: 2 * time.Second,
		SkillsReportInterval:  30 * time.Second,
		RegisterRetryInterval: 5 * time.Second,
		ProcessStopTimeout:    20 * time.Second,
		GatewayStartupTimeout: defaults.GatewayStartupTimeout,
	}
	backendOrigin, err := normalizePublicOrigin(cfg.BackendURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse ClawManager backend origin: %w", err)
	}
	cfg.AllowedOrigins = uniqueOrigins(backendOrigin)
	cfg.PublicOrigin = backendOrigin
	cfg.TrustedProxies = trustedProxiesFromEnvOrPodIP(cfg.PodIP)
	llmSettings, err := llmconfig.LoadFromEnv(llmconfig.ResolveOptions{})
	if err != nil {
		return Config{}, err
	}
	cfg = llmSettings.ApplyTo(cfg)
	if profile := os.Getenv("RUNTIME_NETWORK_SECURITY_PROFILE"); profile != "" {
		if profile != "isolated-network-v1" || os.Getenv("RUNTIME_AGENT_ROLE") != "slot" || cfg.PodUID == "" || len(cfg.ControlToken) < 32 {
			return Config{}, errors.New("invalid isolated network profile, role or management identity")
		}
		count, err := strconv.Atoi(os.Getenv("RUNTIME_NETWORK_SLOT_COUNT"))
		if err != nil || count < 1 || count > 256 {
			return Config{}, errors.New("RUNTIME_NETWORK_SLOT_COUNT must be 1..256")
		}
		index, err := strconv.Atoi(os.Getenv("RUNTIME_SLOT_INDEX"))
		if err != nil || index < 0 || index >= count {
			return Config{}, errors.New("RUNTIME_SLOT_INDEX is outside network capacity")
		}
		cfg.NetworkSecurityProfile, cfg.NetworkSlotCount, cfg.NetworkSlotIndex = profile, count, index
		cfg.NetworkEgressProxyURL = envOrDefault("RUNTIME_NETWORK_EGRESS_PROXY_URL", "http://clawmanager-egress-proxy."+namespace+".svc.cluster.local:3129")
		endpoint, err := url.Parse(cfg.NetworkEgressProxyURL)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
			return Config{}, errors.New("invalid authenticated egress proxy endpoint")
		}
	}

	if raw := os.Getenv("CLAWMANAGER_LLM_VISION"); strings.TrimSpace(raw) != "" {
		vision, err := gateway.ParseVisionCapabilities(raw)
		if err != nil {
			return Config{}, err
		}
		cfg.LLMVision = vision
	}
	if cfg.ControlToken == "" {
		return Config{}, errors.New("RUNTIME_AGENT_CONTROL_TOKEN is required")
	}
	if cfg.ReportToken == "" && strings.TrimSpace(os.Getenv("RUNTIME_AGENT_ROLE")) != "slot" {
		return Config{}, errors.New("RUNTIME_AGENT_REPORT_TOKEN is required")
	}
	if cfg.ImageRef == "" {
		return Config{}, errors.New("CLAWMANAGER_RUNTIME_IMAGE_REF is required")
	}
	if len(cfg.GatewayCommand) == 0 {
		return Config{}, errors.New("gateway command is required")
	}
	if cfg.GatewayPortStart, err = intEnv("RUNTIME_GATEWAY_PORT_START", defaults.GatewayPortStart); err != nil {
		return Config{}, err
	}
	if cfg.GatewayPortEnd, err = intEnv("RUNTIME_GATEWAY_PORT_END", defaults.GatewayPortEnd); err != nil {
		return Config{}, err
	}
	if cfg.GatewayPortEnd < cfg.GatewayPortStart {
		return Config{}, errors.New("RUNTIME_GATEWAY_PORT_END must be greater than or equal to start")
	}
	if cfg.GatewayPortBlockSize, err = intEnv("RUNTIME_GATEWAY_PORT_BLOCK_SIZE", defaults.GatewayPortBlockSize); err != nil {
		return Config{}, err
	}
	if cfg.GatewayPortBlockSize <= 0 {
		return Config{}, errors.New("RUNTIME_GATEWAY_PORT_BLOCK_SIZE must be positive")
	}
	if cfg.Capacity, err = intEnv("RUNTIME_GATEWAY_CAPACITY", defaults.GatewayCapacity); err != nil {
		return Config{}, err
	}
	if cfg.Capacity <= 0 {
		return Config{}, errors.New("RUNTIME_GATEWAY_CAPACITY must be positive")
	}
	if cfg.HeartbeatInterval, err = durationEnv("RUNTIME_AGENT_HEARTBEAT_INTERVAL", cfg.HeartbeatInterval); err != nil {
		return Config{}, err
	}
	if cfg.HeartbeatInterval <= 0 {
		return Config{}, errors.New("RUNTIME_AGENT_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.MetricsInterval, err = durationEnv("RUNTIME_AGENT_METRICS_INTERVAL", cfg.MetricsInterval); err != nil {
		return Config{}, err
	}
	if cfg.GatewayReportInterval, err = durationEnv("RUNTIME_AGENT_GATEWAY_REPORT_INTERVAL", cfg.GatewayReportInterval); err != nil {
		return Config{}, err
	}
	if cfg.SkillsReportInterval, err = durationEnv("RUNTIME_AGENT_SKILLS_REPORT_INTERVAL", cfg.SkillsReportInterval); err != nil {
		return Config{}, err
	}
	if cfg.ProcessStopTimeout, err = durationEnv("RUNTIME_GATEWAY_STOP_TIMEOUT", cfg.ProcessStopTimeout); err != nil {
		return Config{}, err
	}
	if cfg.GatewayStartupTimeout, err = durationEnv("RUNTIME_GATEWAY_STARTUP_HEALTH_TIMEOUT", cfg.GatewayStartupTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RegisterRetryInterval, err = durationEnv("RUNTIME_AGENT_REGISTER_RETRY_INTERVAL", cfg.RegisterRetryInterval); err != nil {
		return Config{}, err
	}

	scheme := "http"
	if strings.TrimSpace(os.Getenv("RUNTIME_AGENT_ROLE")) == "pool" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			return Config{}, errors.New("pool management requires RUNTIME_AGENT_TLS_CERT_FILE and RUNTIME_AGENT_TLS_KEY_FILE")
		}
		scheme = "https"
	}
	cfg.AgentEndpoint = scheme + "://" + net.JoinHostPort(cfg.PodIP, strconv.Itoa(cfg.PublicPort))
	if finalizer, ok := profile.(interface{ FinalizeConfig(Config) (Config, error) }); ok {
		return finalizer.FinalizeConfig(cfg)
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trustedProxiesFromEnvOrPodIP(podIP string) []string {
	configured := splitStringList(envFirst(
		"OPENCLAW_TRUSTED_PROXIES",
		"OPENCLAW_GATEWAY_TRUSTED_PROXIES",
		"CLAWMANAGER_TRUSTED_PROXIES",
		"CLAWMANAGER_GATEWAY_TRUSTED_PROXIES",
	))
	if len(configured) > 0 {
		if len(configured) == 1 && strings.EqualFold(configured[0], "none") {
			return nil
		}
		return configured
	}
	if inferred := inferPodNetworkCIDR(podIP); inferred != "" {
		return []string{inferred}
	}
	return nil
}

func splitStringList(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	}) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func inferPodNetworkCIDR(podIP string) string {
	ip := net.ParseIP(strings.TrimSpace(podIP)).To4()
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.0.0/16", ip[0], ip[1])
}

func intEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return value, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return value, nil
}

func commandFromEnv(profile gateway.RuntimeProfile, authMode string) []string {
	raw := strings.TrimSpace(envOrDefault("RUNTIME_GATEWAY_COMMAND", ""))
	if raw == "" {
		raw = strings.TrimSpace(envOrDefault("OPENCLAW_AGENT_OPENCLAW_COMMAND", ""))
	}
	if raw == "" {
		return profile.GatewayCommand(authMode)
	}
	return normalizeGatewayCommandAuthMode(strings.Fields(raw), authMode)
}

func normalizeGatewayCommandAuthMode(command []string, authMode string) []string {
	if len(command) == 0 {
		return command
	}
	normalized := append([]string(nil), command...)
	for index, arg := range normalized {
		if arg == "--auth" {
			if index+1 < len(normalized) {
				normalized[index+1] = authMode
				return normalized
			}
			return append(normalized, authMode)
		}
		if strings.HasPrefix(arg, "--auth=") {
			normalized[index] = "--auth=" + authMode
			return normalized
		}
	}
	return append(normalized, "--auth", authMode)
}

func normalizePublicOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw == "*" {
		return "*", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("expected absolute origin, got %q", raw)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func uniqueOrigins(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func readNamespaceFile() string {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func firstNonLoopbackIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		return ip.String()
	}
	return ""
}

func deploymentName(podName, runtimeType string) string {
	// The control plane owns this identity. Pod names can be truncated by
	// Kubernetes and cannot reliably reconstruct long upgrade deployment names.
	if value := strings.TrimSpace(envOrDefault("CLAWMANAGER_RUNTIME_DEPLOYMENT_NAME", "")); value != "" {
		return value
	}
	if value := strings.TrimSpace(envOrDefault("CLAWMANAGER_DEPLOYMENT_NAME", "")); value != "" {
		return value
	}
	if value := strings.TrimSpace(envOrDefault("DEPLOYMENT_NAME", "")); value != "" {
		return value
	}
	parts := strings.Split(podName, "-")
	if len(parts) > 2 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	return runtimeType + "-runtime"
}
