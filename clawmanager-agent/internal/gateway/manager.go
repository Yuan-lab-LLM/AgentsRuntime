package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type gatewayRecord struct {
	state   GatewayState
	process ManagedProcess
}

type GatewayManager struct {
	cfg     Config
	starter ProcessStarter
	ports   *PortAllocator
	health  GatewayHealthChecker

	mu       sync.RWMutex
	draining bool
	gateways map[string]*gatewayRecord
	changes  chan struct{}
}

func NewGatewayManager(cfg Config, starter ProcessStarter, ports *PortAllocator) *GatewayManager {
	var health GatewayHealthChecker = noopGatewayHealthChecker{}
	if starter == nil {
		starter = NewExecProcessStarter(cfg)
		if profileHealth := runtimeProfile(cfg).HealthChecker(cfg); profileHealth != nil {
			health = profileHealth
		} else {
			health = NewHTTPGatewayHealthChecker(cfg)
		}
	}
	if ports == nil {
		ports = NewPortAllocator(nil)
	}
	if cfg.GatewayPortBlockSize > 0 {
		ports.SetBlockSize(cfg.GatewayPortBlockSize)
	}
	return &GatewayManager{
		cfg:      cfg,
		starter:  starter,
		ports:    ports,
		health:   health,
		gateways: map[string]*gatewayRecord{},
		changes:  make(chan struct{}, 1),
	}
}

func (m *GatewayManager) SetHealthChecker(health GatewayHealthChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if health == nil {
		health = noopGatewayHealthChecker{}
	}
	m.health = health
}

func (m *GatewayManager) CreateGateway(_ context.Context, req CreateGatewayRequest) (CreateGatewayResponse, error) {
	if strings.ToLower(strings.TrimSpace(req.AgentType)) != m.cfg.RuntimeType {
		return CreateGatewayResponse{}, ErrRuntimeType
	}
	workspacePath, err := ValidateWorkspacePath(m.cfg.WorkspaceRoot, m.cfg.RuntimeType, req)
	if err != nil {
		return CreateGatewayResponse{}, err
	}
	rng := req.PortRange
	if rng.Start == 0 && rng.End == 0 {
		rng = PortRange{Start: m.cfg.GatewayPortStart, End: m.cfg.GatewayPortEnd}
	}

	m.mu.Lock()

	if m.draining {
		m.mu.Unlock()
		return CreateGatewayResponse{}, ErrDraining
	}

	gatewayID := gatewayID(req.InstanceID, req.Generation)
	if existing, ok := m.gateways[gatewayID]; ok {
		resp := createGatewayResponse(existing.state)
		m.mu.Unlock()
		return resp, nil
	}

	var oldProcesses []ManagedProcess
	for id, record := range m.gateways {
		if record.state.InstanceID != req.InstanceID {
			continue
		}
		if record.state.Generation > req.Generation {
			m.mu.Unlock()
			return CreateGatewayResponse{}, ErrStaleGeneration
		}
		if record.state.Generation < req.Generation {
			oldProcesses = append(oldProcesses, m.detachGatewayLocked(id))
		}
	}

	capacity := m.effectiveCapacityLocked()
	if capacity <= 0 || m.usedSlotsLocked() >= capacity {
		var reserveErr error = ErrNoFreePort
		if req.GatewayPort > 0 {
			reserveErr = fmt.Errorf("requested gateway port %d is unavailable: capacity exhausted: %w", req.GatewayPort, ErrNoFreePort)
			log.Printf("runtime-agent reserve requested gateway port failed: instance_id=%d generation=%d gateway_port=%d: %v", req.InstanceID, req.Generation, req.GatewayPort, reserveErr)
		}
		m.mu.Unlock()
		for _, process := range oldProcesses {
			m.stopProcessAsync(process)
		}
		return CreateGatewayResponse{}, reserveErr
	}

	var port int
	if req.GatewayPort > 0 {
		port, err = m.ports.ReserveExact(req.InstanceID, req.Generation, req.GatewayPort)
	} else {
		port, err = m.ports.Reserve(req.InstanceID, req.Generation, rng)
	}
	if err != nil {
		if req.GatewayPort > 0 {
			log.Printf("runtime-agent reserve requested gateway port failed: instance_id=%d generation=%d gateway_port=%d: %v", req.InstanceID, req.Generation, req.GatewayPort, err)
		}
		m.mu.Unlock()
		for _, process := range oldProcesses {
			m.stopProcessAsync(process)
		}
		return CreateGatewayResponse{}, err
	}

	now := time.Now().UTC()
	state := GatewayState{
		InstanceID:    req.InstanceID,
		UserID:        req.UserID,
		GatewayID:     gatewayID,
		RuntimeType:   m.cfg.RuntimeType,
		WorkspacePath: workspacePath,
		Port:          port,
		PortAlias:     port,
		UID:           req.UID,
		GID:           req.GID,
		CPUCores:      req.CPUCores,
		MemoryMB:      req.MemoryMB,
		DiskQuotaMB:   req.DiskQuotaMB,
		Generation:    req.Generation,
		State:         "starting",
		StartedAt:     now,
		UpdatedAt:     now,
	}
	m.gateways[gatewayID] = &gatewayRecord{state: state}
	m.notifyGatewayStateChangedLocked()
	resp := createGatewayResponse(state)
	m.mu.Unlock()

	for _, process := range oldProcesses {
		m.stopProcessAsync(process)
	}
	go m.startGatewayInBackground(gatewayID, req, workspacePath, port)

	return resp, nil
}

func (m *GatewayManager) startGatewayInBackground(gatewayID string, req CreateGatewayRequest, workspacePath string, port int) {
	startedAt := time.Now()
	phaseStartedAt := startedAt
	if err := m.profile().PrepareWorkspace(m.cfg, req, workspacePath); err != nil {
		log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=prepare_workspace phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), err)
		m.markGatewayError(gatewayID, 0, err)
		return
	}
	prepareDuration := time.Since(phaseStartedAt)
	phaseStartedAt = time.Now()
	if err := m.profile().WriteGatewayConfig(m.cfg, req, workspacePath, port); err != nil {
		log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=write_config phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), err)
		m.markGatewayError(gatewayID, 0, err)
		return
	}
	configDuration := time.Since(phaseStartedAt)

	spec := GatewayStartSpec{
		GatewayID:     gatewayID,
		RuntimeType:   m.cfg.RuntimeType,
		InstanceID:    req.InstanceID,
		UserID:        req.UserID,
		WorkspacePath: workspacePath,
		Port:          port,
		UID:           req.UID,
		GID:           req.GID,
		CPUCores:      req.CPUCores,
		MemoryMB:      req.MemoryMB,
		DiskQuotaMB:   req.DiskQuotaMB,
		Generation:    req.Generation,
		Command:       append([]string(nil), m.cfg.GatewayCommand...),
		Env:           m.profile().GatewayEnv(os.Environ(), m.cfg, req, workspacePath, port),
	}
	phaseStartedAt = time.Now()
	process, err := m.starter.StartGateway(context.Background(), spec)
	if err != nil {
		log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=start_process phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), err)
		m.markGatewayError(gatewayID, 0, fmt.Errorf("%w: %v", ErrGatewayStartFailed, err))
		return
	}
	processStartDuration := time.Since(phaseStartedAt)
	if !m.attachGatewayProcess(gatewayID, process) {
		m.stopProcessAsync(process)
		return
	}

	phaseStartedAt = time.Now()
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	healthResult := make(chan error, 1)
	go func() {
		healthResult <- m.health.WaitReady(healthCtx, spec)
	}()
	if process.Done != nil {
		select {
		case processErr := <-process.Done:
			cancelHealth()
			if processErr == nil {
				processErr = fmt.Errorf("gateway process exited before readiness")
			}
			m.markGatewayError(gatewayID, process.PID, fmt.Errorf("%w: %v", ErrGatewayStartFailed, processErr))
			return
		case healthErr := <-healthResult:
			cancelHealth()
			if healthErr != nil {
				log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=wait_ready phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), healthErr)
				m.stopProcessAsync(process)
				m.markGatewayError(gatewayID, process.PID, fmt.Errorf("%w: %v", ErrGatewayStartFailed, healthErr))
				return
			}
		}
		select {
		case processErr := <-process.Done:
			if processErr == nil {
				processErr = fmt.Errorf("gateway process exited at readiness boundary")
			}
			m.markGatewayError(gatewayID, process.PID, fmt.Errorf("%w: %v", ErrGatewayStartFailed, processErr))
			return
		default:
		}
	} else {
		healthErr := <-healthResult
		cancelHealth()
		if healthErr != nil {
			log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=wait_ready phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), healthErr)
			m.stopProcessAsync(process)
			m.markGatewayError(gatewayID, process.PID, fmt.Errorf("%w: %v", ErrGatewayStartFailed, healthErr))
			return
		}
	}
	healthDuration := time.Since(phaseStartedAt)
	m.markGatewayRunning(gatewayID, req, process.PID)
	log.Printf("runtime-agent gateway ready: gateway_id=%s instance_id=%d port=%d pid=%d total_ms=%d prepare_ms=%d config_ms=%d process_ms=%d health_ms=%d", gatewayID, req.InstanceID, port, process.PID, time.Since(startedAt).Milliseconds(), prepareDuration.Milliseconds(), configDuration.Milliseconds(), processStartDuration.Milliseconds(), healthDuration.Milliseconds())
	if process.Done != nil {
		go m.watchGatewayProcess(gatewayID, process.Done)
	}
}

func createGatewayResponse(state GatewayState) CreateGatewayResponse {
	var pid *int
	if state.PID > 0 {
		value := state.PID
		pid = &value
	}
	return CreateGatewayResponse{
		GatewayID:     state.GatewayID,
		InstanceID:    state.InstanceID,
		Port:          state.Port,
		PID:           pid,
		Status:        state.State,
		WorkspacePath: state.WorkspacePath,
	}
}

func (m *GatewayManager) DeleteGateway(ctx context.Context, gatewayID string) error {
	m.mu.Lock()
	process := m.detachGatewayLocked(gatewayID)
	m.mu.Unlock()
	if process.Stop != nil {
		_ = process.Stop(ctx)
	}
	return nil
}

func (m *GatewayManager) SetDraining(draining bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.draining = draining
}

func (m *GatewayManager) Draining() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.draining
}

func (m *GatewayManager) UsedSlots() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.usedSlotsLocked()
}

func (m *GatewayManager) GatewayStates() []GatewayState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]GatewayState, 0, len(m.gateways))
	for _, record := range m.gateways {
		states = append(states, record.state)
	}
	return states
}

func (m *GatewayManager) GatewayStateChanges() <-chan struct{} {
	return m.changes
}

func (m *GatewayManager) Health() error {
	if m.cfg.WorkspaceRoot == "" {
		return fmt.Errorf("workspace root is empty")
	}
	if err := os.MkdirAll(m.cfg.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("workspace root unavailable: %w", err)
	}
	return nil
}

func (m *GatewayManager) profile() RuntimeProfile {
	return runtimeProfile(m.cfg)
}

func runtimeProfile(cfg Config) RuntimeProfile {
	if cfg.Runtime != nil {
		return cfg.Runtime
	}
	return openClawCompatProfile{}
}

func (m *GatewayManager) HeartbeatPayload(podID int) HeartbeatPayload {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := "ready"
	if m.draining {
		state = "draining"
	}
	usedSlots := m.usedSlotsLocked()
	maxGateways := m.effectiveCapacityLocked()
	return HeartbeatPayload{
		PodID:          podID,
		Namespace:      m.cfg.Namespace,
		PodName:        m.cfg.PodName,
		State:          state,
		MaxGateways:    maxGateways,
		UsedSlots:      usedSlots,
		AvailableSlots: maxInt(0, maxGateways-usedSlots),
		Draining:       m.draining,
		ReportedAt:     time.Now().UTC(),
	}
}

func (m *GatewayManager) RegisterPayload() RegisterPayload {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := "ready"
	if m.draining {
		state = "draining"
	}
	usedSlots := m.usedSlotsLocked()
	maxGateways := m.effectiveCapacityLocked()
	return RegisterPayload{
		RuntimeType:    m.cfg.RuntimeType,
		Namespace:      m.cfg.Namespace,
		PodName:        m.cfg.PodName,
		PodUID:         m.cfg.PodUID,
		PodIP:          m.cfg.PodIP,
		NodeName:       m.cfg.NodeName,
		DeploymentName: m.cfg.DeploymentName,
		ImageRef:       m.cfg.ImageRef,
		AgentEndpoint:  m.cfg.AgentEndpoint,
		State:          state,
		Capacity:       maxGateways,
		MaxGateways:    maxGateways,
		UsedSlots:      usedSlots,
		AvailableSlots: maxInt(0, maxGateways-usedSlots),
		Draining:       m.draining,
		ReportedAt:     time.Now().UTC(),
	}
}

func (m *GatewayManager) GatewayReportPayload(podID int) GatewayReportPayload {
	return GatewayReportPayload{
		PodID:     podID,
		Namespace: m.cfg.Namespace,
		PodName:   m.cfg.PodName,
		Gateways:  m.GatewayStates(),
	}
}

func (m *GatewayManager) stopGatewayLocked(ctx context.Context, id string) {
	process := m.detachGatewayLocked(id)
	if process.Stop != nil {
		_ = process.Stop(ctx)
	}
}

func (m *GatewayManager) detachGatewayLocked(id string) ManagedProcess {
	record, ok := m.gateways[id]
	if !ok {
		return ManagedProcess{}
	}
	m.ports.Release(record.state.Port)
	delete(m.gateways, id)
	m.notifyGatewayStateChangedLocked()
	return record.process
}

func (m *GatewayManager) attachGatewayProcess(id string, process ManagedProcess) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok {
		return false
	}
	now := time.Now().UTC()
	record.process = process
	record.state.PID = process.PID
	record.state.UpdatedAt = now
	return true
}

func (m *GatewayManager) markGatewayRunning(id string, req CreateGatewayRequest, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	m.ports.Commit(req.InstanceID, req.Generation, record.state.Port)
	record.state.PID = pid
	record.state.State = "running"
	record.state.ErrorMessage = resourceLimitDegradation(req)
	record.state.HealthAt = now
	record.state.UpdatedAt = now
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) markGatewayError(id string, pid int, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	m.ports.Release(record.state.Port)
	if pid > 0 {
		record.state.PID = pid
	}
	record.process = ManagedProcess{}
	record.state.State = "error"
	record.state.ErrorMessage = cause.Error()
	record.state.HealthAt = now
	record.state.UpdatedAt = now
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) watchGatewayProcess(id string, done <-chan error) {
	err := <-done
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	m.ports.Release(record.state.Port)
	record.process = ManagedProcess{}
	record.state.UpdatedAt = now
	record.state.HealthAt = now
	if err != nil {
		record.state.State = "error"
		record.state.ErrorMessage = err.Error()
		m.notifyGatewayStateChangedLocked()
		return
	}
	record.state.State = "stopped"
	record.state.ErrorMessage = ""
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) notifyGatewayStateChangedLocked() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}

func (m *GatewayManager) stopProcessAsync(process ManagedProcess) {
	if process.Stop == nil {
		return
	}
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), m.stopTimeout())
		_ = process.Stop(stopCtx)
		cancel()
	}()
}

func (m *GatewayManager) usedSlotsLocked() int {
	count := 0
	for _, record := range m.gateways {
		switch record.state.State {
		case "running", "starting":
			count++
		}
	}
	return count
}

func (m *GatewayManager) effectiveCapacityLocked() int {
	portCapacity := portBlockCapacity(PortRange{Start: m.cfg.GatewayPortStart, End: m.cfg.GatewayPortEnd}, m.cfg.GatewayPortBlockSize)
	if m.cfg.Capacity <= 0 {
		return portCapacity
	}
	if portCapacity <= 0 {
		return 0
	}
	return minInt(m.cfg.Capacity, portCapacity)
}

func portBlockCapacity(rng PortRange, blockSize int) int {
	if rng.Start <= 0 || rng.End < rng.Start {
		return 0
	}
	if blockSize <= 0 {
		blockSize = 1
	}
	return (rng.End - rng.Start + 1) / blockSize
}

func gatewayID(instanceID, generation int) string {
	return "gw-" + strconv.Itoa(instanceID) + "-" + strconv.Itoa(generation)
}

func (m *GatewayManager) stopTimeout() time.Duration {
	if m.cfg.ProcessStopTimeout > 0 {
		return m.cfg.ProcessStopTimeout
	}
	return 20 * time.Second
}

func OpenClawGatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = ApplyRequestEnvironment(env, req)
	env = ApplyLiteTeamConfigEnvironment(env, req, workspacePath)
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_AGENT_PERSISTENT_DIR", filepath.Join(workspacePath, "home", ".openclaw"))
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	env = setEnv(env, "HOME", filepath.Join(workspacePath, "home"))
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))
	env = setEnv(env, "OPENCLAW_HOST", "0.0.0.0")
	env = setEnv(env, "OPENCLAW_PORT", strconv.Itoa(port))
	env = setEnv(env, "OPENCLAW_GATEWAY_PORT", strconv.Itoa(port))
	if cfg.GatewayAuthMode == "trusted-proxy" {
		env = unsetEnv(env, "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")
		// OpenClaw's local Browser client connects directly to this instance's
		// loopback Gateway. Give that interactive surface the same per-instance
		// secret that ClawManager already manages instead of trusting every
		// process in a pooled Runtime through allowLoopback.
		if instanceToken := strings.TrimSpace(req.Environment["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = setEnv(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		} else if instanceToken := strings.TrimSpace(req.Env["CLAWMANAGER_INSTANCE_TOKEN"]); instanceToken != "" {
			env = setEnv(env, "OPENCLAW_GATEWAY_PASSWORD", instanceToken)
		}
	} else if cfg.GatewayToken != "" {
		env = setEnv(env, "OPENCLAW_GATEWAY_TOKEN", cfg.GatewayToken)
	}
	return env
}

func GenericGatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = ApplyRequestEnvironment(env, req)
	env = ApplyLiteTeamConfigEnvironment(env, req, workspacePath)
	env = setEnv(env, "CLAWMANAGER_INSTANCE_ID", strconv.Itoa(req.InstanceID))
	env = setEnv(env, "CLAWMANAGER_USER_ID", strconv.Itoa(req.UserID))
	env = setEnv(env, "CLAWMANAGER_RUNTIME_TYPE", cfg.RuntimeType)
	env = setEnv(env, "CLAWMANAGER_WORKSPACE_PATH", workspacePath)
	env = setEnv(env, "CLAWMANAGER_GATEWAY_PORT", strconv.Itoa(port))
	env = setEnv(env, "HOME", filepath.Join(workspacePath, "home"))
	env = setEnv(env, "HOST", "0.0.0.0")
	env = setEnv(env, "PORT", strconv.Itoa(port))
	if cfg.GatewayAuthMode == "trusted-proxy" {
		env = unsetEnv(env, "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "RUNTIME_GATEWAY_TOKEN")
	} else if cfg.GatewayToken != "" {
		env = setEnv(env, "RUNTIME_GATEWAY_TOKEN", cfg.GatewayToken)
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
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

func resourceLimitDegradation(req CreateGatewayRequest) string {
	if req.CPUCores == 0 && req.MemoryMB == 0 && req.DiskQuotaMB == 0 {
		return ""
	}
	return "resource limit enforcement is degraded: cgroup CPU/memory and filesystem quota are not configured by this runtime-agent build"
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type ExecProcessStarter struct {
	cfg Config
}

func NewExecProcessStarter(cfg Config) *ExecProcessStarter {
	return &ExecProcessStarter{cfg: cfg}
}

func (s *ExecProcessStarter) StartGateway(ctx context.Context, spec GatewayStartSpec) (ManagedProcess, error) {
	if len(spec.Command) == 0 {
		return ManagedProcess{}, fmt.Errorf("gateway command is empty")
	}
	if err := os.MkdirAll(filepath.Join(spec.WorkspacePath, "home"), 0o750); err != nil {
		return ManagedProcess{}, fmt.Errorf("create gateway home: %w", err)
	}

	command := LiteTeamGatewayCommand(spec.RuntimeType, spec.Command, spec.Env)
	cmd := exec.CommandContext(context.Background(), command[0], command[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.WorkspacePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureGatewayCommand(cmd, spec.UID, spec.GID)

	if err := cmd.Start(); err != nil {
		return ManagedProcess{}, err
	}
	done := make(chan error, 1)
	notifyDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		done <- err
		notifyDone <- err
	}()

	return ManagedProcess{
		PID:  cmd.Process.Pid,
		Done: notifyDone,
		Stop: func(stopCtx context.Context) error {
			timeout := s.cfg.ProcessStopTimeout
			if timeout <= 0 {
				timeout = 20 * time.Second
			}
			return stopGatewayCommand(stopCtx, cmd, done, timeout)
		},
	}, nil
}
