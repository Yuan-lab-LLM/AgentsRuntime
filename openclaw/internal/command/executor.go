package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	configmanager "github.com/iamlovingit/clawmanager-openclaw-image/internal/configmanager"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/profiler"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/skills"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

type lifecycleClient interface {
	StartCommand(ctx context.Context, id int, req protocol.CommandStartRequest) error
	FinishCommand(ctx context.Context, id int, req protocol.CommandFinishRequest) error
}

type Executor struct {
	client   lifecycleClient
	process  *process.Manager
	profiler *profiler.Profiler
	config   *configmanager.Manager
	skills   *skills.Manager
	store    *store.Store
	mu       sync.Mutex
}

func New(client lifecycleClient, processManager *process.Manager, profilerInstance *profiler.Profiler, configManager *configmanager.Manager, skillManager *skills.Manager, st *store.Store) *Executor {
	return &Executor{
		client:   client,
		process:  processManager,
		profiler: profilerInstance,
		config:   configManager,
		skills:   skillManager,
		store:    st,
	}
}

func (e *Executor) Execute(ctx context.Context, cmd *protocol.Command) error {
	if cmd == nil {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	timeout := time.Duration(cmd.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := e.client.StartCommand(runCtx, cmd.ID, protocol.CommandStartRequest{
		AgentID:   e.store.Snapshot().AgentID,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}

	result, execErr := e.handle(runCtx, cmd)
	finishReq := protocol.CommandFinishRequest{
		AgentID:    e.store.Snapshot().AgentID,
		FinishedAt: time.Now().UTC(),
		Status:     "succeeded",
		Result:     result,
	}
	if execErr != nil {
		finishReq.Status = "failed"
		finishReq.ErrorMessage = execErr.Error()
		finishReq.Result = result
	}

	if err := e.store.Update(func(s *store.State) {
		s.LastCommandExecutionCache[strconv.Itoa(cmd.ID)] = map[string]any{
			"type":        cmd.Type,
			"status":      finishReq.Status,
			"finished_at": finishReq.FinishedAt,
			"result":      finishReq.Result,
			"error":       finishReq.ErrorMessage,
		}
	}); err != nil && execErr == nil {
		execErr = err
	}

	if err := e.client.FinishCommand(context.Background(), cmd.ID, finishReq); err != nil && execErr == nil {
		execErr = err
	}
	return execErr
}

func (e *Executor) handle(ctx context.Context, cmd *protocol.Command) (map[string]any, error) {
	switch cmd.Type {
	case "start_openclaw":
		if err := e.process.Start(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"status": e.process.Snapshot().Status}, nil
	case "stop_openclaw":
		if err := e.process.Stop(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"status": e.process.Snapshot().Status}, nil
	case "restart_openclaw":
		if err := e.process.Restart(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"status": e.process.Snapshot().Status}, nil
	case "collect_system_info":
		return e.profiler.Collect(), nil
	case "apply_config_revision":
		revisionID := revisionIDFromPayload(cmd.Payload)
		if revisionID == "" {
			return nil, errors.New("revision_id is required")
		}
		e.process.MarkConfiguring()
		result, err := e.config.ApplyRevision(ctx, revisionID)
		if err != nil {
			return result, err
		}
		reload, reloadSet := cmd.Payload["reload"].(bool)
		if !reloadSet || reload {
			if err := e.process.Restart(ctx); err != nil {
				return result, err
			}
			result["restarted"] = true
		}
		result["status"] = e.process.Snapshot().Status
		return result, nil
	case "reload_config":
		if err := e.process.Restart(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"status": e.process.Snapshot().Status}, nil
	case "health_check":
		snapshot := e.process.Snapshot()
		return map[string]any{
			"status": snapshot.Status,
			"pid":    snapshot.PID,
			"uptime": snapshot.Uptime.String(),
		}, nil
	case "install_skill":
		result, err := e.skills.Install(ctx, cmd.Payload)
		if err != nil {
			return result, err
		}
		syncResult, syncErr := e.skills.Sync(ctx, "incremental", "command_install_skill", true)
		if syncResult != nil {
			result["inventory_sync"] = syncResult
		}
		return result, syncErr
	case "update_skill":
		result, err := e.skills.Install(ctx, cmd.Payload)
		if err != nil {
			return result, err
		}
		syncResult, syncErr := e.skills.Sync(ctx, "incremental", "command_update_skill", true)
		if syncResult != nil {
			result["inventory_sync"] = syncResult
		}
		return result, syncErr
	case "uninstall_skill", "remove_skill":
		result, err := e.skills.Uninstall(cmd.Payload)
		if err != nil {
			return result, err
		}
		syncResult, syncErr := e.skills.Sync(ctx, "incremental", "command_uninstall_skill", true)
		if syncResult != nil {
			result["inventory_sync"] = syncResult
		}
		return result, syncErr
	case "disable_skill", "quarantine_skill":
		result, err := e.skills.Disable(cmd.Payload)
		if err != nil {
			return result, err
		}
		syncResult, syncErr := e.skills.Sync(ctx, "incremental", "command_disable_skill", true)
		if syncResult != nil {
			result["inventory_sync"] = syncResult
		}
		return result, syncErr
	case "sync_skill_inventory":
		mode := "incremental"
		if full, _ := cmd.Payload["full"].(bool); full {
			mode = "full"
		}
		return e.skills.Sync(ctx, mode, "command_sync_skill_inventory", true)
	case "handle_skill_risk":
		result, err := e.handleSkillRisk(ctx, cmd.Payload)
		if err != nil {
			return result, err
		}
		syncResult, syncErr := e.skills.Sync(ctx, "incremental", "command_handle_skill_risk", true)
		if syncResult != nil {
			result["inventory_sync"] = syncResult
		}
		return result, syncErr
	case "collect_skill_package":
		return e.skills.CollectPackage(ctx, cmd.Payload)
	default:
		return nil, fmt.Errorf("unsupported command type %q", cmd.Type)
	}
}

func (e *Executor) handleSkillRisk(ctx context.Context, payload map[string]any) (map[string]any, error) {
	action := payloadString(payload, "action", "risk_action")
	switch action {
	case "disable", "quarantine":
		result, err := e.skills.Disable(payload)
		if result == nil {
			result = map[string]any{}
		}
		result["risk_action"] = action
		return result, err
	case "remove", "uninstall":
		result, err := e.skills.Uninstall(payload)
		if result == nil {
			result = map[string]any{}
		}
		result["risk_action"] = action
		return result, err
	case "stop_instance", "shutdown":
		err := e.process.Stop(ctx)
		return map[string]any{
			"risk_action": action,
			"status":      e.process.Snapshot().Status,
		}, err
	default:
		return nil, fmt.Errorf("unsupported skill risk action %q", action)
	}
}

func revisionIDFromPayload(payload map[string]any) string {
	if value, ok := payload["revision_id"].(string); ok && value != "" {
		return value
	}
	if value, ok := payload["revision_id"].(float64); ok {
		return strconv.Itoa(int(value))
	}
	return ""
}

func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.Itoa(int(v))
		}
	}
	return ""
}
