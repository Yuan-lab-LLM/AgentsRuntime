package gateway

import (
	"context"
	"errors"
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
	stopOperation        string
	stopBusy             bool
	stopConfirmed        bool
	deletePending        bool
	deleteBusy           bool
	executionCleanup     func(context.Context) error
	state                GatewayState
	process              ManagedProcess
	cancel               context.CancelFunc
	finished             chan struct{}
	reserved             bool
	stopMu               sync.Mutex
	startSpec            GatewayStartSpec
	request              CreateGatewayRequest
	restartAttempts      int
	readyAt              time.Time
	automationStopFailed bool
}

type GatewayManager struct {
	stopEpoch    string
	cfg          Config
	starter      ProcessStarter
	ports        *PortAllocator
	health       GatewayHealthChecker
	capabilities *HealthCapabilities
	// Test seam; production retains the common bounded, jittered cadence.
	nextGatewayHealthCheckDelay func() time.Duration

	mu             sync.RWMutex
	draining       bool
	upgradeStandby bool
	gateways       map[string]*gatewayRecord
	changes        chan struct{}
	clockRequired  bool
	clockCheckedAt time.Time
	clockLastGood  time.Time
	clockIssue     string
	restartDelays  []time.Duration
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
	var capabilities *HealthCapabilities
	if provider, ok := runtimeProfile(cfg).(HealthCapabilityProvider); ok {
		capabilities = CloneHealthCapabilities(provider.HealthCapabilities())
	}
	manager := &GatewayManager{
		stopEpoch:     newStopEpoch(),
		cfg:           cfg,
		starter:       starter,
		ports:         ports,
		health:        health,
		gateways:      map[string]*gatewayRecord{},
		changes:       make(chan struct{}, 1),
		capabilities:  capabilities,
		restartDelays: []time.Duration{time.Second, 3 * time.Second, 10 * time.Second},
	}
	manager.upgradeStandby = manager.shouldStartUpgradeStandby()
	return manager
}

// HealthCapabilities returns a copy of the immutable startup snapshot.
func (m *GatewayManager) HealthCapabilities() *HealthCapabilities {
	return CloneHealthCapabilities(m.capabilities)
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
	if m.IsolatedGatewayLifecycle() {
		return m.createIsolatedGateway(req)
	}
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
	if m.upgradeStandby && (strings.TrimSpace(req.UpgradeID) == "" || strings.TrimSpace(req.UpgradeID) != strings.TrimSpace(m.cfg.UpgradeID)) {
		m.mu.Unlock()
		return CreateGatewayResponse{}, ErrUpgradeStandby
	}
	if blocked, leaseErr := m.writerLeaseBlocksGateway(req.InstanceID); leaseErr != nil {
		m.mu.Unlock()
		return CreateGatewayResponse{}, leaseErr
	} else if blocked {
		m.mu.Unlock()
		return CreateGatewayResponse{}, fmt.Errorf("%w: instance %d", ErrWriterLeaseActive, req.InstanceID)
	}

	gatewayID := gatewayID(req.InstanceID, req.Generation)
	for _, record := range m.gateways {
		if record.state.InstanceID == req.InstanceID && (record.stopOperation != "" || record.deletePending) {
			m.mu.Unlock()
			return CreateGatewayResponse{}, ErrGatewayStopConflict
		}
	}
	if existing, ok := m.gateways[gatewayID]; ok {
		resp := createGatewayResponse(existing.state)
		m.mu.Unlock()
		return resp, nil
	}

	for id, record := range m.gateways {
		if record.state.InstanceID != req.InstanceID {
			continue
		}
		if record.state.Generation > req.Generation {
			m.mu.Unlock()
			return CreateGatewayResponse{}, ErrStaleGeneration
		}
		if record.state.Generation < req.Generation {
			m.mu.Unlock()
			return CreateGatewayResponse{}, fmt.Errorf("%w: gateway_id=%s generation=%d", ErrActiveGeneration, id, record.state.Generation)
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
	m.gateways[gatewayID] = &gatewayRecord{state: state, request: req}
	m.notifyGatewayStateChangedLocked()
	resp := createGatewayResponse(state)
	m.mu.Unlock()

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
		Env:           m.profile().GatewayEnv(m.gatewayBaseEnvironment(), m.cfg, req, workspacePath, port),
	}
	phaseStartedAt = time.Now()
	process, err := m.starter.StartGateway(context.Background(), spec)
	if err != nil {
		log.Printf("runtime-agent gateway startup failed: gateway_id=%s instance_id=%d phase=start_process phase_ms=%d total_ms=%d error=%v", gatewayID, req.InstanceID, time.Since(phaseStartedAt).Milliseconds(), time.Since(startedAt).Milliseconds(), err)
		m.markGatewayError(gatewayID, 0, fmt.Errorf("%w: %v", ErrGatewayStartFailed, err))
		return
	}
	processStartDuration := time.Since(phaseStartedAt)
	if !m.attachGatewayProcess(gatewayID, process, spec) {
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
	automation := m.gatewayAutomations(req)
	m.markGatewayRunning(gatewayID, req, process.PID)
	log.Printf("runtime-agent gateway ready: gateway_id=%s instance_id=%d port=%d pid=%d total_ms=%d prepare_ms=%d config_ms=%d process_ms=%d health_ms=%d", gatewayID, req.InstanceID, port, process.PID, time.Since(startedAt).Milliseconds(), prepareDuration.Milliseconds(), configDuration.Milliseconds(), processStartDuration.Milliseconds(), healthDuration.Milliseconds())
	if automation != nil {
		go m.reconcileAutomationsWithRetry(context.Background(), gatewayID, req, automation, port)
	}
	if process.Done != nil {
		go m.watchGatewayProcess(gatewayID, process.PID, process.Done)
	}
	go m.superviseGateway(context.Background(), gatewayID, spec, process.PID)
}

func (m *GatewayManager) reconcileAutomationsWithRetry(ctx context.Context, gatewayID string, req CreateGatewayRequest, reconcile func(context.Context, int) error, port int) error {
	delays := []time.Duration{0, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	for attempt, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
		if ctx.Err() != nil || !m.gatewayIsRunning(gatewayID) {
			return nil
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		err := reconcile(attemptCtx, port)
		cancel()
		// An auxiliary namespace whose death is unknown is a writer-fence failure,
		// never a warning eligible for another command or automatic restart.
		if errors.Is(err, ErrGatewayStopFailed) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		m.setGatewayAutomationWarning(gatewayID, req, err)
		if err == nil {
			if attempt > 0 {
				log.Printf("runtime-agent automation reconciliation recovered: gateway_id=%s instance_id=%d attempts=%d", gatewayID, req.InstanceID, attempt+1)
			}
			return nil
		}
		log.Printf("runtime-agent automation reconciliation pending: gateway_id=%s instance_id=%d attempt=%d error=%v", gatewayID, req.InstanceID, attempt+1, err)
	}
	return nil
}

func (m *GatewayManager) gatewayAutomationsForAttempt(req CreateGatewayRequest, spec GatewayStartSpec, process ManagedProcess) func(context.Context, int) error {
	provider, ok := m.profile().(ManagedGatewayAutomationProvider)
	if !ok {
		return m.gatewayAutomations(req)
	}
	execute := func(ctx context.Context, command, overrides []string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runner, ok := m.starter.(GatewayCommandRunner)
		if !ok || process.VerifyListeners == nil {
			return nil, fmt.Errorf("managed automation command runner and listener ownership are required")
		}
		// Authenticate only after proving this is still the intended gateway.
		if err := process.VerifyListeners(ctx); err != nil {
			return nil, err
		}
		commandSpec := spec
		commandSpec.Command = append([]string(nil), command...)
		commandSpec.Env = append([]string(nil), spec.Env...)
		for _, entry := range overrides {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || key == "" {
				return nil, fmt.Errorf("invalid managed automation environment")
			}
			commandSpec.Env = setEnv(commandSpec.Env, key, value)
		}
		return runner.RunGatewayCommand(ctx, commandSpec)
	}
	return provider.ManagedGatewayAutomations(m.cfg, req, execute)
}

func (m *GatewayManager) gatewayIsRunning(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.gateways[id]
	return ok && record.state.State == "running"
}

func (m *GatewayManager) setGatewayAutomationWarning(id string, req CreateGatewayRequest, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok || record.state.State != "running" {
		return
	}
	if cause == nil {
		record.state.ErrorMessage = resourceLimitDegradation(req)
	} else {
		record.state.ErrorMessage = "automation reconciliation pending: " + cause.Error()
	}
	record.state.UpdatedAt = time.Now().UTC()
	m.notifyGatewayStateChangedLocked()
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
	if m.IsolatedGatewayLifecycle() {
		return m.deleteIsolatedGateway(ctx, gatewayID)
	}
	return m.StopGatewayConfirmed(ctx, gatewayID)
}

// StopGatewayConfirmed keeps the binding and port reserved until the managed
// process has positively acknowledged termination. This is the safety boundary
// used by stateful OpenClaw upgrades.
func (m *GatewayManager) StopGatewayConfirmed(ctx context.Context, gatewayID string) error {
	// The control API calls this entry point directly. Isolated gateways have
	// one lifecycle owner for cancellation, writer drain and lease release;
	// stopping their process independently races that owner's completion.
	if m.IsolatedGatewayLifecycle() {
		return m.deleteIsolatedGateway(ctx, gatewayID)
	}
	m.mu.Lock()
	record, ok := m.gateways[gatewayID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	if record.stopOperation != "" || record.deleteBusy || record.state.State == "starting" {
		m.mu.Unlock()
		return ErrGatewayStopConflict
	}
	if record.state.State == "stopping" {
		m.mu.Unlock()
		return fmt.Errorf("%w: gateway %s is already stopping", ErrGatewayStopFailed, gatewayID)
	}
	record.deletePending, record.deleteBusy = true, true
	record.state.State = "stopping"
	record.state.UpdatedAt = time.Now().UTC()
	process := record.process
	pid := process.PID
	m.notifyGatewayStateChangedLocked()
	m.mu.Unlock()

	var stopErr error
	if process.Stop != nil {
		stopErr = process.Stop(ctx)
	} else if process.PID > 0 {
		stopErr = ErrGatewayStopUnsupported
	}
	if err := stopErr; err != nil {
		m.mu.Lock()
		if current, exists := m.gateways[gatewayID]; exists && current.process.PID == pid && current.state.State == "stopped" {
			m.ports.Release(current.state.Port)
			delete(m.gateways, gatewayID)
			m.notifyGatewayStateChangedLocked()
			m.mu.Unlock()
			return nil
		}
		if current, exists := m.gateways[gatewayID]; exists && current.process.PID == pid {
			current.deleteBusy = false
			current.state.State = "stop_error"
			current.state.ErrorMessage = err.Error()
			current.state.UpdatedAt = time.Now().UTC()
			m.notifyGatewayStateChangedLocked()
		}
		m.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrGatewayStopFailed, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.gateways[gatewayID]
	if !exists {
		return nil
	}
	if current.process.PID != pid && current.state.State != "stopped" {
		return fmt.Errorf("%w: gateway process changed during stop", ErrGatewayStopFailed)
	}
	m.ports.Release(current.state.Port)
	delete(m.gateways, gatewayID)
	m.notifyGatewayStateChangedLocked()
	return nil
}

func (m *GatewayManager) StopAll(ctx context.Context) error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.gateways))
	for id := range m.gateways {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		if err := m.StopGatewayConfirmed(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *GatewayManager) GatewayState(gatewayID string) (GatewayState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.gateways[gatewayID]
	if !ok {
		return GatewayState{}, false
	}
	return record.state, true
}

func (m *GatewayManager) InstanceActive(instanceID int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instanceActiveLocked(instanceID)
}

func (m *GatewayManager) instanceActiveLocked(instanceID int) bool {
	for _, record := range m.gateways {
		if record.state.InstanceID == instanceID && record.state.State != "stopped" && record.state.State != "error" {
			return true
		}
	}
	return false
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
		state := record.state
		if m.clockRequired && m.clockIssue != "" && (state.State == "running" || state.State == "starting") {
			state.State, state.ErrorMessage = "unhealthy", m.clockIssue
		}
		states = append(states, state)
	}
	return states
}

func (m *GatewayManager) GatewayStateChanges() <-chan struct{} {
	return m.changes
}

func (m *GatewayManager) Health() error {
	m.mu.RLock()
	clockIssue := m.clockIssue
	m.mu.RUnlock()
	if clockIssue != "" {
		return fmt.Errorf("%s", clockIssue)
	}
	if m.cfg.WorkspaceRoot == "" {
		return fmt.Errorf("workspace root is empty")
	}
	if err := os.MkdirAll(m.cfg.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("workspace root unavailable: %w", err)
	}
	if m.IsolatedGatewayLifecycle() {
		if m.cfg.GatewayPortStart < 1 || m.cfg.GatewayPortEnd > 65535 || m.cfg.GatewayPortEnd < m.cfg.GatewayPortStart || (m.cfg.GatewayPortBlockSize > 1 && !m.cfg.IsolatedLifecycle) || (m.cfg.IsolatedLifecycle && m.cfg.GatewayPortBlockSize < 1) {
			return fmt.Errorf("port_conflict: invalid configured pool")
		}
		file, err := os.CreateTemp(m.cfg.WorkspaceRoot, ".runtime-health-*")
		if err != nil {
			return fmt.Errorf("workspace_unavailable")
		}
		file.Close()
		_ = os.Remove(file.Name())
	}
	return nil
}

// ReadyForTraffic is deliberately stricter than Health. An upgrade target can
// expose its authenticated management API while remaining outside Kubernetes
// service readiness until every persistent workspace has passed migration.
func (m *GatewayManager) ReadyForTraffic() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.upgradeStandby
}

func (m *GatewayManager) UpgradeStandby() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.upgradeStandby
}

func (m *GatewayManager) upgradeActivationPath() string {
	return filepath.Join(m.upgradeRoot(), "rollout-"+m.cfg.UpgradeID, "activated")
}

func (m *GatewayManager) shouldStartUpgradeStandby() bool {
	if strings.TrimSpace(m.cfg.UpgradeID) == "" {
		return false
	}
	if validateUpgradeID("upgrade_id", m.cfg.UpgradeID) != nil {
		return true
	}
	_, err := os.Stat(m.upgradeActivationPath())
	return err != nil
}

func (m *GatewayManager) ActivateUpgrade(rolloutID string) error {
	rolloutID = strings.TrimSpace(rolloutID)
	if err := validateUpgradeID("rollout_id", rolloutID); err != nil {
		return err
	}
	if rolloutID != strings.TrimSpace(m.cfg.UpgradeID) || rolloutID == "" {
		return errors.New("rollout_id does not match this runtime pod")
	}
	path := m.upgradeActivationPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := atomicWriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return err
	}
	m.mu.Lock()
	m.upgradeStandby = false
	m.notifyGatewayStateChangedLocked()
	m.mu.Unlock()
	return nil
}

func (m *GatewayManager) profile() RuntimeProfile {
	return runtimeProfile(m.cfg)
}

func runtimeProfile(cfg Config) RuntimeProfile {
	if cfg.Runtime != nil {
		return cfg.Runtime
	}
	return runtimeNeutralProfile{runtimeType: cfg.RuntimeType}
}

func (m *GatewayManager) HeartbeatPayload(podID int) HeartbeatPayload {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := "ready"
	if m.upgradeStandby {
		state = "standby"
	} else if m.draining {
		state = "draining"
	} else if m.clockRequired && m.clockIssue != "" {
		state = "error"
	}
	usedSlots := m.usedSlotsLocked()
	maxGateways := m.effectiveCapacityLocked()
	availableSlots := maxInt(0, maxGateways-usedSlots)
	if m.upgradeStandby {
		availableSlots = 0
	}
	return HeartbeatPayload{
		PodID:          podID,
		Namespace:      m.cfg.Namespace,
		PodName:        m.cfg.PodName,
		State:          state,
		MaxGateways:    maxGateways,
		UsedSlots:      usedSlots,
		AvailableSlots: availableSlots,
		Draining:       m.draining,
		ReportedAt:     time.Now().UTC(),
	}
}

func (m *GatewayManager) RegisterPayload() RegisterPayload {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := "ready"
	if m.upgradeStandby {
		state = "standby"
	} else if m.draining {
		state = "draining"
	} else if m.clockRequired && m.clockIssue != "" {
		state = "error"
	}
	usedSlots := m.usedSlotsLocked()
	maxGateways := m.effectiveCapacityLocked()
	availableSlots := maxInt(0, maxGateways-usedSlots)
	if m.upgradeStandby {
		availableSlots = 0
	}
	metadata := RuntimeRegistrationMetadata{}
	if provider, ok := m.profile().(RegistrationMetadataProvider); ok {
		metadata = provider.RegistrationMetadata(m.cfg)
	}
	return RegisterPayload{
		RuntimeType:       m.cfg.RuntimeType,
		OpenClawVersion:   metadata.OpenClawVersion,
		Capabilities:      metadata.Capabilities,
		ProtocolVersion:   metadata.ProtocolVersion,
		TeamPluginVersion: metadata.TeamPluginVersion,
		SessionStore:      metadata.SessionStore,
		ImageDigest:       strings.TrimSpace(os.Getenv("CLAWMANAGER_RUNTIME_IMAGE_DIGEST")),
		Namespace:         m.cfg.Namespace,
		PodName:           m.cfg.PodName,
		PodUID:            m.cfg.PodUID,
		PodIP:             m.cfg.PodIP,
		NodeName:          m.cfg.NodeName,
		DeploymentName:    m.cfg.DeploymentName,
		ImageRef:          m.cfg.ImageRef,
		AgentEndpoint:     m.cfg.AgentEndpoint,
		State:             state,
		Capacity:          maxGateways,
		MaxGateways:       maxGateways,
		UsedSlots:         usedSlots,
		AvailableSlots:    availableSlots,
		Draining:          m.draining,
		ReportedAt:        time.Now().UTC(),
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
	if !ok || record.stopOperation != "" || record.deletePending {
		return ManagedProcess{}
	}
	m.ports.Release(record.state.Port)
	delete(m.gateways, id)
	m.notifyGatewayStateChangedLocked()
	return record.process
}

func (m *GatewayManager) attachGatewayProcess(id string, process ManagedProcess, spec GatewayStartSpec) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok {
		return false
	}
	now := time.Now().UTC()
	record.process = process
	record.startSpec = spec
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
	record.state.FailureClass = ""
	record.state.ExitCode = nil
	record.state.Retryable = nil
	record.state.RestartAttempt = 0
	record.state.HealthAt = now
	record.state.UpdatedAt = now
	record.readyAt = now
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
	record.state.FailureClass = "gateway_start_failed"
	record.state.ExitCode = processExitCode(cause)
	retryable := false
	record.state.Retryable = &retryable
	record.state.HealthAt = now
	record.state.UpdatedAt = now
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) watchGatewayProcess(id string, pid int, done <-chan error) {
	err := <-done
	m.mu.Lock()
	record, ok := m.gateways[id]
	if !ok || record.process.PID != pid || (record.process.Done != nil && record.process.Done != done) || record.stopOperation != "" {
		m.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	if record.state.State == "stopping" {
		// StopGatewayConfirmed owns the binding and port until it has observed
		// the matching process exit.  The watcher may win the scheduling race,
		// but it must not erase the PID that identifies that process.
		record.state.State = "stopped"
		record.state.ErrorMessage = ""
		record.state.UpdatedAt = now
		record.state.HealthAt = now
		m.notifyGatewayStateChangedLocked()
		m.mu.Unlock()
		return
	}
	if record.deletePending {
		m.mu.Unlock()
		return
	}
	if err == nil {
		err = errors.New("gateway process exited unexpectedly")
	}
	if !record.readyAt.IsZero() && now.Sub(record.readyAt) >= 5*time.Minute {
		record.restartAttempts = 0
	}
	if m.automaticGatewayRecoveryEnabled() && len(m.restartDelays) > 0 && record.restartAttempts < len(m.restartDelays) {
		record.restartAttempts++
		attempt := record.restartAttempts
		delay := m.restartDelays[attempt-1]
		record.process = ManagedProcess{}
		record.state.PID = 0
		record.state.State = "starting"
		record.state.ErrorMessage = fmt.Sprintf("recovering after unexpected gateway exit (attempt %d/%d): %v", attempt, len(m.restartDelays), err)
		record.state.FailureClass = "unexpected_process_exit"
		record.state.ExitCode = processExitCode(err)
		retryable := true
		record.state.Retryable = &retryable
		record.state.RestartAttempt = attempt
		record.state.UpdatedAt = now
		record.state.HealthAt = now
		m.notifyGatewayStateChangedLocked()
		m.mu.Unlock()
		go m.restartGatewayAfterUnexpectedExit(id, attempt, delay)
		return
	}
	m.ports.Release(record.state.Port)
	record.process = ManagedProcess{}
	record.state.PID = 0
	record.state.UpdatedAt = now
	record.state.HealthAt = now
	record.state.State = "error"
	record.state.ErrorMessage = fmt.Sprintf("gateway exited and automatic recovery was exhausted after %d attempts: %v", record.restartAttempts, err)
	record.state.FailureClass = "unexpected_process_exit"
	record.state.ExitCode = processExitCode(err)
	retryable := false
	record.state.Retryable = &retryable
	record.state.RestartAttempt = record.restartAttempts
	m.notifyGatewayStateChangedLocked()
	m.mu.Unlock()
}

func (m *GatewayManager) restartGatewayAfterUnexpectedExit(id string, attempt int, delay time.Duration) {
	if delay > 0 {
		timer := time.NewTimer(delay)
		<-timer.C
	}
	m.mu.RLock()
	record, ok := m.gateways[id]
	if !ok || record.state.State != "starting" || record.restartAttempts != attempt || record.process.PID != 0 {
		m.mu.RUnlock()
		return
	}
	spec := record.startSpec
	req := record.request
	m.mu.RUnlock()

	process, err := m.starter.StartGateway(context.Background(), spec)
	if err != nil {
		m.finishGatewayRestartAttempt(id, attempt, fmt.Errorf("restart process: %w", err))
		return
	}
	if !m.attachRestartedGatewayProcess(id, attempt, process) {
		m.stopProcessAsync(process)
		return
	}

	healthCtx, cancelHealth := context.WithCancel(context.Background())
	healthResult := make(chan error, 1)
	go func() { healthResult <- m.health.WaitReady(healthCtx, spec) }()
	if process.Done != nil {
		select {
		case processErr := <-process.Done:
			cancelHealth()
			if processErr == nil {
				processErr = errors.New("gateway process exited before recovery readiness")
			}
			m.finishGatewayRestartAttempt(id, attempt, processErr)
			return
		case healthErr := <-healthResult:
			cancelHealth()
			if healthErr != nil {
				if stopErr := m.stopGatewayRecoveryProcess(process); stopErr != nil {
					m.failGatewayRecoveryWithoutFence(id, attempt, fmt.Errorf("restart readiness: %v; process stop was not confirmed: %w", healthErr, stopErr))
					return
				}
				m.finishGatewayRestartAttempt(id, attempt, fmt.Errorf("restart readiness: %w", healthErr))
				return
			}
		}
		select {
		case processErr := <-process.Done:
			if processErr == nil {
				processErr = errors.New("gateway process exited at recovery readiness boundary")
			}
			m.finishGatewayRestartAttempt(id, attempt, processErr)
			return
		default:
		}
	} else {
		healthErr := <-healthResult
		cancelHealth()
		if healthErr != nil {
			if stopErr := m.stopGatewayRecoveryProcess(process); stopErr != nil {
				m.failGatewayRecoveryWithoutFence(id, attempt, fmt.Errorf("restart readiness: %v; process stop was not confirmed: %w", healthErr, stopErr))
				return
			}
			m.finishGatewayRestartAttempt(id, attempt, fmt.Errorf("restart readiness: %w", healthErr))
			return
		}
	}
	m.markRestartedGatewayRunning(id, attempt, process.PID)
	if automation := m.gatewayAutomations(req); automation != nil {
		go m.reconcileAutomationsWithRetry(context.Background(), id, req, automation, spec.Port)
	}
	if process.Done != nil {
		go m.watchGatewayProcess(id, process.PID, process.Done)
	}
	go m.superviseGateway(context.Background(), id, spec, process.PID)
}

func (m *GatewayManager) stopGatewayRecoveryProcess(process ManagedProcess) error {
	if process.Stop == nil {
		return errors.New("managed process does not support confirmed stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout())
	defer cancel()
	return process.Stop(ctx)
}

func (m *GatewayManager) failGatewayRecoveryWithoutFence(id string, attempt int, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok || record.restartAttempts != attempt || record.state.State == "stopping" {
		return
	}
	record.state.State = "error"
	record.state.ErrorMessage = cause.Error()
	record.state.FailureClass = "restart_stop_unconfirmed"
	record.state.ExitCode = processExitCode(cause)
	retryable := false
	record.state.Retryable = &retryable
	record.state.RestartAttempt = attempt
	record.state.UpdatedAt = time.Now().UTC()
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) attachRestartedGatewayProcess(id string, attempt int, process ManagedProcess) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok || record.state.State != "starting" || record.restartAttempts != attempt || record.process.PID != 0 {
		return false
	}
	record.process = process
	record.state.PID = process.PID
	record.state.UpdatedAt = time.Now().UTC()
	m.notifyGatewayStateChangedLocked()
	return true
}

func (m *GatewayManager) markRestartedGatewayRunning(id string, attempt int, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.gateways[id]
	if !ok || record.state.State != "starting" || record.restartAttempts != attempt || record.process.PID != pid {
		return
	}
	now := time.Now().UTC()
	record.state.State = "running"
	record.state.ErrorMessage = ""
	record.state.FailureClass = ""
	record.state.ExitCode = nil
	record.state.Retryable = nil
	record.state.RestartAttempt = 0
	record.state.HealthAt = now
	record.state.UpdatedAt = now
	record.readyAt = now
	m.notifyGatewayStateChangedLocked()
}

func (m *GatewayManager) finishGatewayRestartAttempt(id string, attempt int, cause error) {
	m.mu.Lock()
	record, ok := m.gateways[id]
	if !ok || record.restartAttempts != attempt || record.state.State == "stopping" {
		m.mu.Unlock()
		return
	}
	record.process = ManagedProcess{}
	record.state.PID = 0
	record.state.UpdatedAt = time.Now().UTC()
	if attempt < len(m.restartDelays) {
		record.restartAttempts++
		nextAttempt := record.restartAttempts
		delay := m.restartDelays[nextAttempt-1]
		record.state.State = "starting"
		record.state.ErrorMessage = fmt.Sprintf("recovering after unexpected gateway exit (attempt %d/%d): %v", nextAttempt, len(m.restartDelays), cause)
		record.state.FailureClass = "unexpected_process_exit"
		record.state.ExitCode = processExitCode(cause)
		retryable := true
		record.state.Retryable = &retryable
		record.state.RestartAttempt = nextAttempt
		m.notifyGatewayStateChangedLocked()
		m.mu.Unlock()
		go m.restartGatewayAfterUnexpectedExit(id, nextAttempt, delay)
		return
	}
	m.ports.Release(record.state.Port)
	record.state.State = "error"
	record.state.ErrorMessage = fmt.Sprintf("gateway automatic recovery exhausted after %d attempts: %v", attempt, cause)
	record.state.FailureClass = "unexpected_process_exit"
	record.state.ExitCode = processExitCode(cause)
	retryable := false
	record.state.Retryable = &retryable
	record.state.RestartAttempt = attempt
	m.notifyGatewayStateChangedLocked()
	m.mu.Unlock()
}

func processExitCode(err error) *int {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil
	}
	code := exitErr.ExitCode()
	return &code
}

func (m *GatewayManager) automaticGatewayRecoveryEnabled() bool {
	if m == nil {
		return false
	}
	policy, ok := m.profile().(GatewayRecoveryPolicy)
	return ok && policy.AutomaticGatewayRecovery(m.cfg)
}
func (m *GatewayManager) gatewayAutomations(req CreateGatewayRequest) func(context.Context, int) error {
	if provider, ok := m.profile().(GatewayAutomationProvider); ok {
		return provider.GatewayAutomations(m.cfg, req)
	}
	return nil
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
		if record.stopOperation != "" || record.deletePending {
			count++ // Retain the reserved slot until a future fenced migration releases it.
			continue
		}
		switch record.state.State {
		case "running", "starting", "unhealthy":
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

func GenericGatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string {
	env := append([]string(nil), base...)
	env = ApplyRequestEnvironment(env, req)
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
	if err := validateProxyPolicyForStart(s.cfg); err != nil {
		return ManagedProcess{}, err
	}
	if len(spec.Command) == 0 {
		return ManagedProcess{}, fmt.Errorf("gateway command is empty")
	}
	if err := os.MkdirAll(filepath.Join(spec.WorkspacePath, "home"), 0o750); err != nil {
		return ManagedProcess{}, fmt.Errorf("create gateway home: %w", err)
	}

	command := RuntimeGatewayCommand(s.cfg, spec.Command, spec.Env)
	cmd := exec.CommandContext(context.Background(), command[0], command[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.WorkspacePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if p, ok := s.cfg.Runtime.(interface{ IsolatedGatewayLifecycle() bool }); ok && p.IsolatedGatewayLifecycle() {
		// Third-party output can contain newly minted session tokens that are
		// impossible to redact by an environment-value allowlist. Only emit
		// structured lifecycle diagnostics until upstream logging is audited.
		sink := &isolatedProcessLog{instanceID: spec.InstanceID, generation: spec.Generation}
		cmd.Stdout, cmd.Stderr = sink, sink
	}
	configureGatewayCommand(cmd, spec.UID, spec.GID)

	if err := cmd.Start(); err != nil {
		return ManagedProcess{}, err
	}
	var cleanup func()
	done := make(chan error, 1)
	notifyDone := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(exited)
		done <- err
		notifyDone <- err
		close(done)
		close(notifyDone)
	}()
	var stopMu sync.Mutex
	stopped := false

	process := ManagedProcess{
		GracefulStop: newGracefulGatewayStop(cmd, exited),
		PID:          cmd.Process.Pid,
		Done:         notifyDone,
		Stop: func(stopCtx context.Context) error {
			stopMu.Lock()
			defer stopMu.Unlock()
			if stopped {
				return nil
			}
			timeout := s.cfg.ProcessStopTimeout
			if timeout <= 0 {
				timeout = 20 * time.Second
			}
			var err error
			select {
			case <-exited:
			default:
				err = stopGatewayCommand(stopCtx, cmd, done, timeout)
			}
			select {
			case <-exited:
				err = nil
			default:
			}
			if err == nil {
				stopped = true
				if cleanup != nil {
					cleanup()
				}
			}
			return err
		},
	}
	if p, ok := s.cfg.Runtime.(interface{ IsolatedGatewayLifecycle() bool }); ok && p.IsolatedGatewayLifecycle() {
		var err error
		cleanup, err = recordIsolatedProcess(s.cfg, spec, cmd.Process.Pid)
		if err != nil {
			// Keep a live stop handle even when durable metadata failed. The
			// isolated lifecycle owns cleanup and retains its lease on failure.
			return process, fmt.Errorf("workspace_unavailable: process metadata could not be recorded")
		}
	}
	return process, nil
}
