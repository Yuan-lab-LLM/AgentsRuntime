package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const (
	startupNotificationTimeout  = 60 * time.Second
	startupNotificationInterval = 2 * time.Second
	startupNotificationCmdWait  = time.Second
	startupNotificationTTL      = 60 * time.Second
	desktopNotificationEnvTTL   = 10 * time.Second
	gatewayReadyPollInterval    = time.Second
	gatewayReadyHTTPTimeout     = 2 * time.Second
	gatewayChannelSettleDelay   = 5 * time.Second
	channelsJSONEnv             = "CLAWMANAGER_OPENCLAW_CHANNELS_JSON"
	skipChannelsEnv             = "OPENCLAW_SKIP_CHANNELS"
	gatewayModelsStdoutPath     = "/tmp/gateway-models.json"
	gatewayModelsStderrPath     = "/tmp/gateway-models.err"
)

type Status string

const (
	StatusStarting    Status = "starting"
	StatusRunning     Status = "running"
	StatusStopped     Status = "stopped"
	StatusStopping    Status = "stopping"
	StatusCrashed     Status = "crashed"
	StatusConfiguring Status = "configuring"
	StatusUnknown     Status = "unknown"
)

type Snapshot struct {
	Status               Status        `json:"status"`
	PID                  int           `json:"pid,omitempty"`
	StartedAt            time.Time     `json:"started_at,omitempty"`
	ExitedAt             time.Time     `json:"exited_at,omitempty"`
	LastExitCode         int           `json:"last_exit_code,omitempty"`
	LastExitReason       string        `json:"last_exit_reason,omitempty"`
	LastOperation        string        `json:"last_operation,omitempty"`
	LastOperationResult  string        `json:"last_operation_result,omitempty"`
	GatewayWarmupStarted bool          `json:"gateway_warmup_started"`
	GatewayWarmupReady   bool          `json:"gateway_warmup_ready"`
	Restarts             int           `json:"restarts"`
	Uptime               time.Duration `json:"uptime,omitempty"`
}

type doctorState struct {
	Command         []string  `json:"command"`
	ConfigHash      string    `json:"config_hash"`
	OpenClawVersion string    `json:"openclaw_version"`
	SucceededAt     time.Time `json:"succeeded_at"`
}

type Manager struct {
	cfg appconfig.Config

	opMu                   sync.Mutex
	mu                     sync.RWMutex
	cmd                    *exec.Cmd
	status                 Status
	startedAt              time.Time
	exitedAt               time.Time
	lastExitCode           int
	lastExitReason         string
	lastOperation          string
	lastOperationResult    string
	restarts               int
	done                   chan struct{}
	stopRequested          bool
	notifyMu               sync.Mutex
	notificationID         string
	notificationGeneration int
	notifyEnvMu            sync.Mutex
	notifyEnvs             [][]string
	notifyEnvLoadedAt      time.Time
	warmupMu               sync.Mutex
	warmupStarted          bool
	warmupDone             bool
}

func New(cfg appconfig.Config) *Manager {
	return &Manager{cfg: cfg, status: StatusStopped}
}

func (m *Manager) Start(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.start(ctx, false)
}

func (m *Manager) start(ctx context.Context, skipDoctor bool) error {
	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil && m.status != StatusStopped && m.status != StatusCrashed {
		m.lastOperation = "start_openclaw"
		m.lastOperationResult = "noop_already_running"
		m.mu.Unlock()
		return nil
	}
	if len(m.cfg.OpenClawCommand) == 0 {
		m.mu.Unlock()
		return errors.New("openclaw command is empty")
	}

	m.status = StatusStarting
	m.lastOperation = "start_openclaw"
	m.lastOperationResult = "preparing_start"
	m.mu.Unlock()

	m.notifyStartup(ctx, m.cfg.StartupNotificationMessage)
	doctorRan := false
	if !skipDoctor && m.shouldRunDoctorBeforeStart() {
		m.setLastOperationResult("running_openclaw_doctor")
		m.notifyStartup(ctx, m.cfg.StartupRepairNotificationMessage)
		if err := m.runDoctor(ctx); err != nil {
			m.markStartFailed(err)
			return fmt.Errorf("run openclaw doctor --fix: %w", err)
		}
		doctorRan = true
		if err := m.writeDoctorState(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "openclaw-agent: record doctor state failed: %v\n", err)
		}
		m.notifyStartup(ctx, m.cfg.StartupNotificationMessage)
	}

	m.setLastOperationResult("starting_openclaw_gateway")
	cmd := exec.CommandContext(context.Background(), m.cfg.OpenClawCommand[0], m.cfg.OpenClawCommand[1:]...)
	cmd.Env = openClawStartEnvFromOS()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil && m.status != StatusStopped && m.status != StatusCrashed {
		m.lastOperationResult = "noop_already_running"
		m.mu.Unlock()
		return nil
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		if !skipDoctor && !doctorRan && m.doctorPolicy() == "auto" {
			return m.repairAfterStartFailure(ctx, fmt.Errorf("start openclaw: %w", err))
		}
		m.markStartFailed(err)
		return fmt.Errorf("start openclaw: %w", err)
	}

	m.cmd = cmd
	m.done = make(chan struct{})
	m.stopRequested = false
	m.startedAt = time.Now().UTC()
	m.exitedAt = time.Time{}
	m.lastExitReason = ""
	m.lastOperationResult = "started"
	m.warmupStarted = false
	m.warmupDone = false
	pid := cmd.Process.Pid
	repairOnHealthTimeout := !skipDoctor && !doctorRan && m.doctorPolicy() == "auto"
	m.mu.Unlock()

	go m.wait(cmd, repairOnHealthTimeout)
	go m.promoteRunning(pid, repairOnHealthTimeout)
	m.startGatewayModelsWarmup(m.done)
	return nil
}

func openClawStartEnvFromOS() []string {
	raw, present := os.LookupEnv(channelsJSONEnv)
	return openClawStartEnv(os.Environ(), raw, present)
}

func openClawStartEnv(base []string, channelsRaw string, channelsPresent bool) []string {
	env := append([]string(nil), base...)
	if channelsEnvConfigured(channelsRaw, channelsPresent) {
		return removeEnv(env, skipChannelsEnv)
	}
	// TEMP(testing): do not inject OPENCLAW_SKIP_CHANNELS=1 for gateway subprocess.
	// Restore: return setOrAppend(env, skipChannelsEnv, "1")
	return removeEnv(env, skipChannelsEnv)
}

func channelsEnvConfigured(raw string, present bool) bool {
	return present && strings.TrimSpace(raw) != ""
}

func removeEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) startGatewayModelsWarmup(done <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if done == nil {
			return
		}
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer cancel()
		err := m.runGatewayModelsWarmup(ctx, done)
		if err == nil {
			m.markGatewayModelsWarmupDone(done)
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "openclaw-agent: gateway models warmup failed: %v\n", err)
		}
	}()
}

func (m *Manager) markGatewayModelsWarmupStarted(done <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if done != nil && m.done == done {
		m.warmupStarted = true
	}
}

func (m *Manager) markGatewayModelsWarmupDone(done <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if done != nil && m.done == done {
		m.warmupDone = true
	}
}

func (m *Manager) runGatewayModelsWarmup(ctx context.Context, done <-chan struct{}) error {
	m.warmupMu.Lock()
	defer m.warmupMu.Unlock()

	if err := m.waitForGatewayReady(ctx); err != nil {
		return err
	}
	if delay := gatewayChannelSettleDelayFromOS(); delay > 0 {
		fmt.Fprintf(os.Stderr, "openclaw-agent: gateway HTTP ready; waiting %s before models warmup\n", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	started := time.Now()
	m.markGatewayModelsWarmupStarted(done)
	fmt.Fprintln(os.Stderr, "openclaw-agent: gateway HTTP ready; warming models list")
	if err := m.runGatewayModelsWarmupOnce(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "openclaw-agent: gateway models warmup completed in %s\n", time.Since(started).Round(time.Millisecond))
	return nil
}

func (m *Manager) runGatewayModelsWarmupOnce(ctx context.Context) error {
	bin, args := gatewayModelsWarmupCommand(m.cfg)
	cmd := exec.CommandContext(ctx, bin, args...)

	if err := os.MkdirAll(filepath.Dir(gatewayModelsStdoutPath), 0o755); err != nil {
		return fmt.Errorf("prepare gateway models stdout dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(gatewayModelsStderrPath), 0o755); err != nil {
		return fmt.Errorf("prepare gateway models stderr dir: %w", err)
	}

	stdout, err := os.OpenFile(gatewayModelsStdoutPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open gateway models stdout: %w", err)
	}
	defer stdout.Close()

	stderr, err := os.OpenFile(gatewayModelsStderrPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open gateway models stderr: %w", err)
	}
	defer stderr.Close()

	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

func (m *Manager) waitForGatewayReady(ctx context.Context) error {
	readyURL := gatewayReadyURL(m.cfg.OpenClawHealthURL)
	if readyURL == "" {
		return nil
	}
	client := &http.Client{Timeout: gatewayReadyHTTPTimeout}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		timer := time.NewTimer(gatewayReadyPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func gatewayReadyURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/readyz"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func gatewayModelsWarmupCommand(cfg appconfig.Config) (string, []string) {
	return openClawBinary(cfg), []string{
		"gateway",
		"call",
		"models.list",
		"--params",
		"{}",
		"--timeout",
		"180000",
		"--json",
	}
}

func gatewayChannelSettleDelayFromOS() time.Duration {
	raw, present := os.LookupEnv(channelsJSONEnv)
	if channelsEnvConfigured(raw, present) {
		return gatewayChannelSettleDelay
	}
	return 0
}

func openClawBinary(cfg appconfig.Config) string {
	if len(cfg.OpenClawCommand) > 0 && cfg.OpenClawCommand[0] != "" {
		return cfg.OpenClawCommand[0]
	}
	return "openclaw"
}

func (m *Manager) runDoctor(ctx context.Context) error {
	if len(m.cfg.OpenClawDoctorCommand) == 0 {
		return nil
	}
	command := lowerPriorityCommand(m.cfg.OpenClawDoctorCommand)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func lowerPriorityCommand(command []string) []string {
	next := append([]string(nil), command...)
	if len(next) == 0 {
		return next
	}
	if ionice, err := exec.LookPath("ionice"); err == nil {
		next = append([]string{ionice, "-c", "3"}, next...)
	}
	if nice, err := exec.LookPath("nice"); err == nil {
		next = append([]string{nice, "-n", "10"}, next...)
	}
	return next
}

func (m *Manager) shouldRunDoctorBeforeStart() bool {
	switch m.doctorPolicy() {
	case "always":
		return true
	default:
		return false
	}
}

func (m *Manager) doctorPolicy() string {
	switch strings.ToLower(strings.TrimSpace(m.cfg.OpenClawDoctorPolicy)) {
	case "always", "never", "auto":
		return strings.ToLower(strings.TrimSpace(m.cfg.OpenClawDoctorPolicy))
	default:
		return "auto"
	}
}

func (m *Manager) writeDoctorState(ctx context.Context) error {
	state, err := m.buildDoctorState(ctx)
	if err != nil {
		return err
	}
	state.SucceededAt = time.Now().UTC()
	if err := os.MkdirAll(m.cfg.AgentDataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.doctorStatePath(), append(data, '\n'), 0o644)
}

func (m *Manager) buildDoctorState(ctx context.Context) (doctorState, error) {
	configHash, err := fileSHA256(m.cfg.OpenClawConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return doctorState{}, err
	}
	return doctorState{
		Command:         append([]string(nil), m.cfg.OpenClawDoctorCommand...),
		ConfigHash:      configHash,
		OpenClawVersion: m.openClawVersion(ctx),
	}, nil
}

func (m *Manager) doctorStatePath() string {
	return filepath.Join(m.cfg.AgentDataDir, "openclaw-doctor-state.json")
}

func (m *Manager) openClawVersion(ctx context.Context) string {
	bin := "openclaw"
	if len(m.cfg.OpenClawCommand) > 0 && m.cfg.OpenClawCommand[0] != "" {
		bin = m.cfg.OpenClawCommand[0]
	} else if len(m.cfg.OpenClawDoctorCommand) > 0 && m.cfg.OpenClawDoctorCommand[0] != "" {
		bin = m.cfg.OpenClawDoctorCommand[0]
	}

	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionCtx, bin, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileSHA256(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (m *Manager) notifyStartup(_ context.Context, message string) {
	if message == "" {
		return
	}

	m.notifyMu.Lock()
	m.notificationGeneration++
	generation := m.notificationGeneration
	m.notifyMu.Unlock()

	go m.notifyWithRetry(context.Background(), message, generation)
}

func (m *Manager) notifyWithRetry(ctx context.Context, message string, generation int) {
	deadline := time.NewTimer(startupNotificationTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupNotificationInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		if !m.isCurrentNotification(generation) {
			return
		}
		if err := m.tryNotifyStartup(ctx, message, generation); err == nil {
			return
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			fmt.Fprintf(os.Stderr, "openclaw-agent: startup notification skipped: %s: %v\n", message, lastErr)
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) tryNotifyStartup(ctx context.Context, message string, generation int) error {
	var lastErr error
	for _, env := range m.desktopNotificationEnvs() {
		if !m.isCurrentNotification(generation) {
			return nil
		}
		notifyCtx, cancel := context.WithTimeout(ctx, startupNotificationCmdWait)
		cmd := exec.CommandContext(notifyCtx, "notify-send", m.notifySendArgs(message)...)
		cmd.Env = env
		out, err := cmd.Output()
		cancel()
		id := strings.TrimSpace(string(out))
		if err == nil && id != "" && id != "0" {
			m.storeNotificationID(generation, id)
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("notify-send returned invalid notification id %q", id)
		}
	}
	return lastErr
}

func (m *Manager) notifySendArgs(message string) []string {
	args := []string{
		"-a", "OpenClaw",
		"-t", fmt.Sprintf("%d", startupNotificationTTL.Milliseconds()),
		"-h", "boolean:transient:true",
		"-p",
	}
	m.notifyMu.Lock()
	if m.notificationID != "" {
		args = append(args, "-r", m.notificationID)
	}
	m.notifyMu.Unlock()
	return append(args, "OpenClaw", message)
}

func (m *Manager) storeNotificationID(generation int, id string) {
	if id == "" {
		return
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	if m.notificationGeneration == generation {
		m.notificationID = id
	}
}

func (m *Manager) isCurrentNotification(generation int) bool {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	return m.notificationGeneration == generation
}

func (m *Manager) desktopNotificationEnvs() [][]string {
	m.notifyEnvMu.Lock()
	if len(m.notifyEnvs) > 0 && time.Since(m.notifyEnvLoadedAt) < desktopNotificationEnvTTL {
		envs := m.notifyEnvs
		m.notifyEnvMu.Unlock()
		return envs
	}
	m.notifyEnvMu.Unlock()

	envs := buildDesktopNotificationEnvs(m.cfg)

	m.notifyEnvMu.Lock()
	m.notifyEnvs = envs
	m.notifyEnvLoadedAt = time.Now()
	m.notifyEnvMu.Unlock()
	return envs
}

func buildDesktopNotificationEnvs(cfg appconfig.Config) [][]string {
	env := os.Environ()
	env = setOrAppend(env, "DISPLAY", ":1")
	env = setOrAppend(env, "HOME", "/config")
	env = setOrAppend(env, "PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	env = setOrAppend(env, "XDG_CURRENT_DESKTOP", "KDE")
	env = setOrAppend(env, "XDG_SESSION_TYPE", "wayland")

	runtimeDir := "/config/.XDG"
	waylandDisplay := "wayland-0"
	if cfg.WaylandSocketPath != "" {
		runtimeDir = filepath.Dir(cfg.WaylandSocketPath)
		waylandDisplay = filepath.Base(cfg.WaylandSocketPath)
	}
	env = setOrAppend(env, "WAYLAND_DISPLAY", waylandDisplay)
	env = setOrAppend(env, "QT_QPA_PLATFORM", "wayland")

	sessionBusPaths := desktopSessionBusPaths()
	runtimeDirs := uniqueStrings([]string{
		os.Getenv("XDG_RUNTIME_DIR"),
		runtimeDir,
		"/run/user/1000",
	})
	envs := make([][]string, 0, len(sessionBusPaths)+len(runtimeDirs)+1)
	for _, busPath := range sessionBusPaths {
		candidate := setOrAppend(env, "XDG_RUNTIME_DIR", runtimeDir)
		candidate = setOrAppend(candidate, "DBUS_SESSION_BUS_ADDRESS", "unix:path="+busPath)
		envs = append(envs, candidate)
	}
	for _, dir := range runtimeDirs {
		if dir == "" {
			continue
		}
		candidate := setOrAppend(env, "XDG_RUNTIME_DIR", dir)
		candidate = setOrAppend(candidate, "DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(dir, "bus"))
		envs = append(envs, candidate)
	}
	envs = append(envs, env)
	return envs
}

func desktopSessionBusPaths() []string {
	matches, err := filepath.Glob("/tmp/dbus-*")
	if err != nil {
		return nil
	}
	type busPath struct {
		path    string
		modTime time.Time
	}
	paths := make([]busPath, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		paths = append(paths, busPath{path: match, modTime: info.ModTime()})
	}
	sort.SliceStable(paths, func(i, j int) bool {
		return paths[i].modTime.Before(paths[j].modTime)
	})
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, path.path)
	}
	return out
}

func setOrAppend(env []string, key, value string) []string {
	prefix := key + "="
	next := append([]string(nil), env...)
	for i, item := range next {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			next[i] = prefix + value
			return next
		}
	}
	return append(next, prefix+value)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (m *Manager) setLastOperationResult(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOperationResult = result
}

func (m *Manager) markStartFailed(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusCrashed
	m.lastOperationResult = err.Error()
	m.lastExitReason = err.Error()
}

func (m *Manager) promoteRunning(pid int, repairOnHealthTimeout bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(m.startupHealthTimeout())
	defer timeout.Stop()

	for {
		select {
		case <-timeout.C:
			if repairOnHealthTimeout && m.cfg.OpenClawHealthURL != "" {
				go m.repairAfterStartupHealthTimeout(pid)
				return
			}
			m.markRunning(pid, "running")
			return
		case <-ticker.C:
			if m.checkHealth() {
				m.markRunning(pid, "running")
				return
			}
		}
	}
}

func (m *Manager) startupHealthTimeout() time.Duration {
	if m.cfg.OpenClawStartupHealthTimeout > 0 {
		return m.cfg.OpenClawStartupHealthTimeout
	}
	return 90 * time.Second
}

func (m *Manager) markRunning(pid int, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil && m.cmd.Process.Pid == pid && m.status == StatusStarting {
		m.status = StatusRunning
		m.lastOperationResult = result
	}
}

func (m *Manager) repairAfterStartupHealthTimeout(pid int) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil || m.cmd.Process.Pid != pid || m.status != StatusStarting {
		m.mu.Unlock()
		return
	}
	m.lastOperationResult = "repairing_after_health_timeout"
	m.mu.Unlock()

	m.notifyStartup(context.Background(), m.cfg.StartupRepairNotificationMessage)

	repairCtx, cancel := context.WithTimeout(context.Background(), m.cfg.ProcessStopTimeout+3*time.Minute)
	defer cancel()
	if err := m.stop(repairCtx); err != nil {
		m.markStartFailed(fmt.Errorf("stop unhealthy openclaw before doctor: %w", err))
		return
	}
	if err := m.runDoctor(repairCtx); err != nil {
		m.markStartFailed(fmt.Errorf("run openclaw doctor --fix after health timeout: %w", err))
		return
	}
	if err := m.writeDoctorState(repairCtx); err != nil {
		fmt.Fprintf(os.Stderr, "openclaw-agent: record doctor state failed: %v\n", err)
	}
	if err := m.start(context.Background(), true); err != nil {
		m.markStartFailed(err)
	}
}

func (m *Manager) repairAfterStartFailure(ctx context.Context, startErr error) error {
	m.setLastOperationResult("repairing_after_start_failure")
	m.notifyStartup(ctx, m.cfg.StartupRepairNotificationMessage)

	repairCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := m.runDoctor(repairCtx); err != nil {
		wrapped := fmt.Errorf("%w; doctor after start failure also failed: %v", startErr, err)
		m.markStartFailed(wrapped)
		return wrapped
	}
	if err := m.writeDoctorState(repairCtx); err != nil {
		fmt.Fprintf(os.Stderr, "openclaw-agent: record doctor state failed: %v\n", err)
	}
	return m.start(context.Background(), true)
}

func (m *Manager) repairAfterStartupExit(pid int, exitReason string) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.RLock()
	if m.cmd != nil || (m.status != StatusCrashed && m.status != StatusStopped) || m.lastExitReason != exitReason {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	m.setLastOperationResult("repairing_after_startup_exit")
	m.notifyStartup(context.Background(), m.cfg.StartupRepairNotificationMessage)

	repairCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := m.runDoctor(repairCtx); err != nil {
		m.markStartFailed(fmt.Errorf("run openclaw doctor --fix after startup exit pid=%d: %w", pid, err))
		return
	}
	if err := m.writeDoctorState(repairCtx); err != nil {
		fmt.Fprintf(os.Stderr, "openclaw-agent: record doctor state failed: %v\n", err)
	}
	if err := m.start(context.Background(), true); err != nil {
		m.markStartFailed(err)
	}
}

func (m *Manager) Stop(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.stop(ctx)
}

func (m *Manager) stop(ctx context.Context) error {
	m.mu.Lock()
	cmd := m.cmd
	done := m.done
	if cmd == nil || cmd.Process == nil || m.status == StatusStopped {
		m.lastOperation = "stop_openclaw"
		m.lastOperationResult = "noop_already_stopped"
		m.status = StatusStopped
		m.mu.Unlock()
		return nil
	}
	m.status = StatusStopping
	m.lastOperation = "stop_openclaw"
	m.stopRequested = true
	m.mu.Unlock()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		m.mu.Lock()
		m.lastOperationResult = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("signal openclaw: %w", err)
	}

	timer := time.NewTimer(m.cfg.ProcessStopTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		m.mu.Lock()
		m.lastOperationResult = ctx.Err().Error()
		m.mu.Unlock()
		return ctx.Err()
	case <-done:
		m.mu.Lock()
		m.lastOperationResult = "stopped"
		m.mu.Unlock()
		return nil
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			m.mu.Lock()
			m.lastOperationResult = err.Error()
			m.mu.Unlock()
			return fmt.Errorf("kill openclaw: %w", err)
		}
		select {
		case <-done:
			m.mu.Lock()
			m.lastOperationResult = "killed_after_timeout"
			m.mu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("openclaw did not exit after kill")
		}
	}
}

func (m *Manager) Restart(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.stop(ctx); err != nil {
		return err
	}
	return m.start(ctx, true)
}

func (m *Manager) MarkConfiguring() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusConfiguring
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Snapshot{
		Status:               m.status,
		StartedAt:            m.startedAt,
		ExitedAt:             m.exitedAt,
		LastExitCode:         m.lastExitCode,
		LastExitReason:       m.lastExitReason,
		LastOperation:        m.lastOperation,
		LastOperationResult:  m.lastOperationResult,
		GatewayWarmupStarted: m.warmupStarted,
		GatewayWarmupReady:   m.warmupDone,
		Restarts:             m.restarts,
	}
	if m.cmd != nil && m.cmd.Process != nil {
		s.PID = m.cmd.Process.Pid
	}
	if !m.startedAt.IsZero() && (m.status == StatusRunning || m.status == StatusStarting || m.status == StatusConfiguring) {
		s.Uptime = time.Since(m.startedAt)
	}
	return s
}

func (m *Manager) checkHealth() bool {
	readyURL := gatewayReadyURL(m.cfg.OpenClawHealthURL)
	if readyURL == "" {
		return true
	}
	req, err := http.NewRequest(http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (m *Manager) wait(cmd *exec.Cmd, repairOnStartupExit bool) {
	err := cmd.Wait()

	m.mu.Lock()

	wasStarting := m.status == StatusStarting
	m.exitedAt = time.Now().UTC()
	if cmd.ProcessState != nil {
		m.lastExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		m.status = StatusCrashed
		m.lastExitReason = err.Error()
	} else if m.stopRequested || m.status == StatusStopping {
		m.status = StatusStopped
		m.lastExitReason = "stopped_by_agent"
	} else if m.status != StatusStopped {
		m.status = StatusStopped
		m.lastExitReason = "process_exited"
	}
	exitReason := m.lastExitReason
	shouldRepair := repairOnStartupExit && wasStarting && !m.stopRequested && m.doctorPolicy() == "auto"
	if m.startedAt.Add(30 * time.Second).Before(m.exitedAt) {
		m.restarts = 0
	} else {
		m.restarts++
	}
	m.cmd = nil
	m.stopRequested = false
	if m.done != nil {
		close(m.done)
		m.done = nil
	}
	m.mu.Unlock()

	if shouldRepair {
		go m.repairAfterStartupExit(cmd.Process.Pid, exitReason)
	}
}
