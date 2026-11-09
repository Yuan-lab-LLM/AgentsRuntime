package instanceagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/heartbeat"
)

type Agent struct {
	cfg    Config
	client *apiClient
	logger *slog.Logger

	mu              sync.Mutex
	session         Session
	lastHeartbeatAt time.Time
	lastState       *StateReport
	lastInventory   []SkillInfo
	lastSkillScanAt time.Time
	skillIndex      map[string]SkillInfo

	commandMu sync.Mutex
}

func New(cfg Config, logger *slog.Logger) *Agent {
	return &Agent{
		cfg:        cfg,
		client:     newAPIClient(cfg),
		logger:     logger,
		skillIndex: map[string]SkillInfo{},
	}
}

func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkDir(), "logs"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkDir(), "cache"), 0o755); err != nil {
		return err
	}
	if err := a.loadSession(); err != nil {
		a.logger.Warn("session cache ignored", "error", err)
	}

	if a.cfg.HTTPAddr != "" {
		go func() {
			if err := a.runLocalServer(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("local gin server stopped", "error", err)
			}
		}()
	}

	a.logger.Info("Hermes runtime agent started", "agent_id", a.cfg.AgentID, "instance_id", a.cfg.InstanceID)

	a.tryInitialReports(ctx)

	heartbeatTimer := time.NewTimer(heartbeat.NextDelay(a.cfg.HeartbeatEvery))
	commandTimer := time.NewTimer(2 * time.Second)
	stateTimer := time.NewTimer(8 * time.Second)
	skillTimer := time.NewTimer(15 * time.Second)
	defer heartbeatTimer.Stop()
	defer commandTimer.Stop()
	defer stateTimer.Stop()
	defer skillTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeatTimer.C:
			pending, err := a.sendHeartbeat(ctx)
			if err != nil {
				a.logger.Warn("heartbeat failed", "error", err)
			}
			if pending {
				go a.processNextCommand(context.WithoutCancel(ctx))
			}
			heartbeatTimer.Reset(heartbeat.NextDelay(a.cfg.HeartbeatEvery))
		case <-commandTimer.C:
			go a.processNextCommand(context.WithoutCancel(ctx))
			commandTimer.Reset(a.cfg.CommandPollEvery)
		case <-stateTimer.C:
			if err := a.reportState(ctx); err != nil {
				a.logger.Warn("state report failed", "error", err)
			}
			stateTimer.Reset(a.cfg.StateReportEvery)
		case <-skillTimer.C:
			if _, err := a.syncSkillInventory(ctx, "incremental", "periodic"); err != nil {
				a.logger.Warn("skill inventory sync failed", "error", err)
			}
			skillTimer.Reset(a.cfg.SkillScanEvery)
		}
	}
}

func (a *Agent) tryInitialReports(ctx context.Context) {
	if err := a.ensureSession(ctx); err != nil {
		a.logger.Warn("initial registration failed", "error", err)
		return
	}
	if err := a.reportState(ctx); err != nil {
		a.logger.Warn("initial state report failed", "error", err)
	}
	if _, err := a.syncSkillInventory(ctx, "full", "startup"); err != nil {
		a.logger.Warn("initial skill inventory failed", "error", err)
	}
}

func (a *Agent) ensureSession(ctx context.Context) error {
	a.mu.Lock()
	hasToken := a.session.SessionToken != ""
	a.mu.Unlock()
	if hasToken {
		return nil
	}
	return a.register(ctx)
}

func (a *Agent) register(ctx context.Context) error {
	response, err := a.client.register(ctx)
	if err != nil {
		return err
	}
	if response.SessionToken == "" {
		return errors.New("register response did not include session_token")
	}

	session := Session{
		AgentID:                    a.cfg.AgentID,
		SessionToken:               response.SessionToken,
		RegisteredAt:               time.Now().UTC(),
		HeartbeatIntervalSeconds:   response.HeartbeatIntervalSeconds,
		CommandPollIntervalSeconds: response.CommandPollIntervalSeconds,
	}

	a.mu.Lock()
	a.session = session
	if response.HeartbeatIntervalSeconds > 0 {
		a.cfg.HeartbeatEvery = time.Duration(response.HeartbeatIntervalSeconds) * time.Second
	}
	if response.CommandPollIntervalSeconds > 0 {
		a.cfg.CommandPollEvery = time.Duration(response.CommandPollIntervalSeconds) * time.Second
	}
	if response.StateReportIntervalSeconds > 0 {
		a.cfg.StateReportEvery = time.Duration(response.StateReportIntervalSeconds) * time.Second
	}
	if response.SkillScanIntervalSeconds > 0 {
		a.cfg.SkillScanEvery = time.Duration(response.SkillScanIntervalSeconds) * time.Second
	}
	a.mu.Unlock()

	if err := a.saveSession(session); err != nil {
		a.logger.Warn("failed to persist session", "error", err)
	}
	a.logger.Info("registered with ClawManager", "agent_id", a.cfg.AgentID)
	return nil
}

func (a *Agent) withSession(ctx context.Context, call func(token string) error) error {
	if err := a.ensureSession(ctx); err != nil {
		return err
	}

	token := a.sessionToken()
	err := call(token)
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}

	a.logger.Warn("session expired, re-registering")
	a.clearSession()
	if err := a.register(ctx); err != nil {
		return err
	}
	return call(a.sessionToken())
}

func (a *Agent) sendHeartbeat(ctx context.Context) (bool, error) {
	state, summary := CollectRuntimeState(a.cfg, a.lastSkillScan())
	skillCount := a.currentSkillCount()
	summary["skill_count"] = skillCount
	summary["active_skill_count"] = skillCount
	body := HeartbeatBody{
		AgentID:        a.cfg.AgentID,
		Timestamp:      time.Now().UTC(),
		OpenClawStatus: state.Runtime.OpenClawStatus,
		Summary:        summary,
	}

	var response heartbeatResponse
	err := a.withSession(ctx, func(token string) error {
		var callErr error
		response, callErr = a.client.heartbeat(ctx, token, body)
		return callErr
	})
	if err != nil {
		return false, err
	}

	a.mu.Lock()
	a.lastHeartbeatAt = time.Now().UTC()
	if response.HeartbeatIntervalSeconds > 0 {
		a.cfg.HeartbeatEvery = time.Duration(response.HeartbeatIntervalSeconds) * time.Second
	}
	if response.CommandPollIntervalSeconds > 0 {
		a.cfg.CommandPollEvery = time.Duration(response.CommandPollIntervalSeconds) * time.Second
	}
	a.mu.Unlock()

	return response.HasPendingCommand, nil
}

func (a *Agent) reportState(ctx context.Context) error {
	state, _ := CollectRuntimeState(a.cfg, a.lastSkillScan())
	state.AgentID = a.cfg.AgentID
	state.ReportedAt = time.Now().UTC()
	return a.reportStateSnapshot(ctx, state)
}

func (a *Agent) reportStateSnapshot(ctx context.Context, state StateReport) error {
	err := a.withSession(ctx, func(token string) error {
		return a.client.reportState(ctx, token, state)
	})
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.lastState = &state
	a.mu.Unlock()
	return nil
}

func (a *Agent) syncSkillInventory(ctx context.Context, mode, trigger string) ([]SkillInfo, error) {
	skills, err := ScanSkills(a.cfg.SkillDirs)
	if err != nil {
		return nil, err
	}

	body := SkillInventoryBody{
		AgentID:    a.cfg.AgentID,
		ReportedAt: time.Now().UTC(),
		Mode:       mode,
		Trigger:    trigger,
		Skills:     skills,
	}
	err = a.withSession(ctx, func(token string) error {
		return a.client.reportSkills(ctx, token, body)
	})
	if err != nil {
		return nil, err
	}

	index := make(map[string]SkillInfo, len(skills)*2)
	for _, skill := range skills {
		index[skill.SkillID] = skill
		index[skill.Identifier] = skill
	}

	a.mu.Lock()
	a.lastInventory = skills
	a.skillIndex = index
	a.lastSkillScanAt = time.Now().UTC()
	a.mu.Unlock()

	a.logger.Info("skill inventory synced", "mode", mode, "trigger", trigger, "count", len(skills))
	return skills, nil
}

func (a *Agent) processNextCommand(ctx context.Context) {
	if !a.commandMu.TryLock() {
		return
	}
	defer a.commandMu.Unlock()

	var cmd *Command
	err := a.withSession(ctx, func(token string) error {
		var callErr error
		cmd, callErr = a.client.nextCommand(ctx, token)
		return callErr
	})
	if err != nil {
		a.logger.Warn("command poll failed", "error", err)
		return
	}
	if cmd == nil {
		return
	}
	if cmd.ID == "" || cmd.Type == "" {
		a.logger.Warn("invalid command skipped", "id", cmd.ID, "type", cmd.Type)
		return
	}

	a.logger.Info("command received", "id", cmd.ID, "type", cmd.Type)

	err = a.withSession(ctx, func(token string) error {
		return a.client.startCommand(ctx, token, cmd.ID, a.cfg.AgentID)
	})
	if err != nil {
		a.logger.Warn("command start failed", "id", cmd.ID, "error", err)
		return
	}

	status := "succeeded"
	result, execErr := a.executeCommand(ctx, cmd)
	errorMessage := ""
	if execErr != nil {
		status = "failed"
		errorMessage = execErr.Error()
		result = map[string]any{}
	}

	finish := commandFinishBody{
		AgentID:      a.cfg.AgentID,
		Status:       status,
		FinishedAt:   time.Now().UTC(),
		Result:       result,
		ErrorMessage: errorMessage,
	}
	if err := a.withSession(ctx, func(token string) error {
		return a.client.finishCommand(ctx, token, cmd.ID, finish)
	}); err != nil {
		a.logger.Warn("command finish failed", "id", cmd.ID, "error", err)
	}

	if execErr != nil {
		a.logger.Warn("command failed", "id", cmd.ID, "type", cmd.Type, "error", execErr)
		return
	}
	a.logger.Info("command finished", "id", cmd.ID, "type", cmd.Type)
	_ = a.reportState(ctx)
}

func (a *Agent) executeCommand(ctx context.Context, cmd *Command) (map[string]any, error) {
	switch cmd.Type {
	case "collect_system_info":
		state, _ := CollectRuntimeState(a.cfg, a.lastSkillScan())
		if err := a.reportStateSnapshot(ctx, state); err != nil {
			return nil, err
		}
		return map[string]any{"sampled_at": state.SystemInfo.SampledAt, "system_info": state.SystemInfo, "runtime": state.Runtime}, nil
	case "health_check":
		state, _ := CollectRuntimeState(a.cfg, a.lastSkillScan())
		if err := a.reportStateSnapshot(ctx, state); err != nil {
			return nil, err
		}
		return map[string]any{"health": state.Health, "system_info": map[string]any{"sampled_at": state.SystemInfo.SampledAt}}, nil
	case "sync_skill_inventory", "refresh_skill_inventory":
		skills, err := a.syncSkillInventory(ctx, "full", cmd.Type)
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": "skill inventory refreshed", "skill_count": len(skills)}, nil
	case "install_skill", "update_skill":
		skillVersion := firstPayloadString(cmd.Payload, "skill_version", "skill_version_id")
		if skillVersion == "" {
			return nil, errors.New("skill_version is required")
		}
		var archive []byte
		if err := a.withSession(ctx, func(token string) error {
			var downloadErr error
			archive, downloadErr = a.client.downloadSkill(ctx, token, skillVersion)
			return downloadErr
		}); err != nil {
			return nil, err
		}
		result, err := InstallSkillArchive(a.cfg.SkillInstallDir, cmd.Payload, archive)
		if err != nil {
			return nil, err
		}
		if _, err := a.syncSkillInventory(ctx, "incremental", cmd.Type); err != nil {
			return nil, err
		}
		return result, nil
	case "uninstall_skill", "remove_skill":
		result, err := UninstallSkill(a.cfg.SkillInstallDir, cmd.Payload)
		if err != nil {
			return nil, err
		}
		if _, err := a.syncSkillInventory(ctx, "incremental", cmd.Type); err != nil {
			return nil, err
		}
		return result, nil
	case "collect_skill_package":
		skill, err := a.findCommandSkill(ctx, cmd)
		if err != nil {
			return nil, err
		}
		zipPath, cleanup, err := CreateSkillPackage(skill)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return nil, err
		}
		if err := a.withSession(ctx, func(token string) error {
			return a.client.uploadSkill(ctx, token, skill, zipPath)
		}); err != nil {
			return nil, err
		}
		return map[string]any{
			"message":     "skill package uploaded",
			"skill_id":    skill.SkillID,
			"identifier":  skill.Identifier,
			"content_md5": skill.ContentMD5,
		}, nil
	case "start_openclaw", "stop_openclaw", "restart_openclaw":
		return map[string]any{"message": "ignored compatibility command for Hermes runtime"}, nil
	default:
		return nil, fmt.Errorf("unsupported command type: %s", cmd.Type)
	}
}

func (a *Agent) findCommandSkill(ctx context.Context, cmd *Command) (SkillInfo, error) {
	candidates := payloadStrings(cmd.Payload, "skill_id", "identifier", "install_path", "content_md5")

	a.mu.Lock()
	for _, key := range candidates {
		if skill, ok := a.skillIndex[key]; ok {
			a.mu.Unlock()
			return skill, nil
		}
	}
	a.mu.Unlock()

	skills, err := a.syncSkillInventory(ctx, "full", "collect_skill_package")
	if err != nil {
		return SkillInfo{}, err
	}

	for _, skill := range skills {
		if len(candidates) == 0 {
			return skill, nil
		}
		for _, key := range candidates {
			if skill.SkillID == key || skill.Identifier == key || skill.InstallPath == key || skill.ContentMD5 == key {
				return skill, nil
			}
		}
	}
	return SkillInfo{}, fmt.Errorf("skill not found for command payload")
}

func (a *Agent) loadSession() error {
	data, err := os.ReadFile(a.sessionPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return err
	}
	if session.SessionToken == "" {
		return nil
	}
	if session.AgentID != "" && session.AgentID != a.cfg.AgentID {
		return nil
	}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	return nil
}

func (a *Agent) saveSession(session Session) error {
	if err := os.MkdirAll(a.cfg.WorkDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.sessionPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.sessionPath())
}

func (a *Agent) clearSession() {
	a.mu.Lock()
	a.session = Session{}
	a.mu.Unlock()
	_ = os.Remove(a.sessionPath())
}

func (a *Agent) sessionPath() string {
	return filepath.Join(a.cfg.WorkDir(), "session.json")
}

func (a *Agent) sessionToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session.SessionToken
}

func (a *Agent) lastSkillScan() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastSkillScanAt
}

func (a *Agent) currentSkillCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.lastInventory)
}

func (a *Agent) Snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"agent_id":           a.cfg.AgentID,
		"registered":         a.session.SessionToken != "",
		"last_heartbeat_at":  a.lastHeartbeatAt,
		"last_skill_scan_at": a.lastSkillScanAt,
		"skill_count":        len(a.lastInventory),
		"last_state":         a.lastState,
	}
}

func payloadStrings(payload map[string]any, keys ...string) []string {
	var values []string
	seen := map[string]bool{}
	for _, key := range keys {
		if value, ok := payload[key]; ok && value != nil {
			text := fmt.Sprint(value)
			if text != "" && !seen[text] {
				values = append(values, text)
				seen[text] = true
			}
		}
	}
	return values
}
