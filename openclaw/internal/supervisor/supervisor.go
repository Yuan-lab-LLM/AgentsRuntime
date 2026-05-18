package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-openclaw-image/internal/command"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	configmanager "github.com/iamlovingit/clawmanager-openclaw-image/internal/configmanager"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/control"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/httpserver"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/openclawinspector"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/profiler"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/session"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/skills"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

type Supervisor struct {
	cfg       appconfig.Config
	store     *store.Store
	process   *process.Manager
	profiler  *profiler.Profiler
	inspector *openclawinspector.Inspector
	client    *control.Client
	session   *session.Manager
	config    *configmanager.Manager
	skills    *skills.Manager
	executor  *command.Executor
	http      *httpserver.Server
	logger    *log.Logger
}

func New(cfg appconfig.Config) (*Supervisor, error) {
	if err := os.MkdirAll(cfg.AgentDataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFilePath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}

	logWriter, err := os.OpenFile(cfg.LogFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	logger := log.New(logWriter, "openclaw-agent ", log.LstdFlags|log.LUTC)

	st, err := store.New(cfg.AgentDataDir)
	if err != nil {
		return nil, err
	}

	client := control.New(cfg.ControlPlaneBaseURL, cfg.BootstrapToken, func() string {
		return st.Snapshot().SessionToken
	})
	proc := process.New(cfg)
	prof := profiler.New(cfg)
	inspector := openclawinspector.New(cfg.OpenClawConfigPath, cfg.OpenClawWorkspacePath, cfg.OpenClawBuiltinSkillsPath)
	sessionManager := session.New(cfg, client, st)
	configManager := configmanager.New(cfg, client, st)
	skillManager := skills.New(cfg, client, st)
	executor := command.New(client, proc, prof, configManager, skillManager, st)
	httpServer := httpserver.New(cfg.LocalHTTPBind, proc, prof, inspector, st)

	return &Supervisor{
		cfg:       cfg,
		store:     st,
		process:   proc,
		profiler:  prof,
		inspector: inspector,
		client:    client,
		session:   sessionManager,
		config:    configManager,
		skills:    skillManager,
		executor:  executor,
		http:      httpServer,
		logger:    logger,
	}, nil
}

func (s *Supervisor) Run(ctx context.Context) error {
	s.logger.Printf("starting instance_id=%s", s.cfg.InstanceID)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 5)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.http.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	if err := s.config.NormalizeActiveConfig(); err != nil {
		return fmt.Errorf("normalize openclaw config: %w", err)
	}

	if err := s.ensureSession(ctx); err != nil {
		return err
	}

	state := s.store.Snapshot()
	if state.CurrentConfigRevisionID == "" && s.cfg.InitialConfigRevisionID != "" {
		if _, err := s.config.ApplyRevision(ctx, s.cfg.InitialConfigRevisionID); err != nil {
			return fmt.Errorf("apply initial openclaw config revision: %w", err)
		}
		s.logger.Printf("initial openclaw config revision applied revision_id=%s", s.cfg.InitialConfigRevisionID)
	}

	if err := s.config.CaptureModelBaseline(); err != nil {
		return fmt.Errorf("capture openclaw model baseline: %w", err)
	}
	s.logger.Printf("openclaw model guard armed")

	if snapshot := s.process.Snapshot(); snapshot.Status == process.StatusStopped || snapshot.Status == process.StatusUnknown {
		if err := s.process.Start(ctx); err != nil {
			return fmt.Errorf("start openclaw on boot: %w", err)
		}
		s.logger.Printf("openclaw bootstrap start issued")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.modelGuardLoop(ctx)
	}()

	wg.Add(4)
	go func() {
		defer wg.Done()
		s.heartbeatLoop(ctx, errCh)
	}()
	go func() {
		defer wg.Done()
		s.reportLoop(ctx, errCh)
	}()
	go func() {
		defer wg.Done()
		s.commandLoop(ctx, errCh)
	}()
	go func() {
		defer wg.Done()
		s.skillLoop(ctx, errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}

	wg.Wait()
	return nil
}

func (s *Supervisor) ensureSession(ctx context.Context) error {
	for {
		_, err := s.session.Ensure(ctx)
		if err == nil {
			return nil
		}
		s.logger.Printf("register failed: %v", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.cfg.RegisterRetryInterval):
		}
	}
}

func (s *Supervisor) heartbeatLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.ensureSession(ctx); err != nil {
				errCh <- err
				return
			}
			snapshot := s.process.Snapshot()
			state := s.store.Snapshot()
			inspect := s.inspector.Collect()
			_, err := s.client.Heartbeat(ctx, protocol.HeartbeatRequest{
				AgentID:                 state.AgentID,
				Timestamp:               time.Now().UTC(),
				OpenClawStatus:          string(snapshot.Status),
				CurrentConfigRevisionID: parseOptionalInt(state.CurrentConfigRevisionID),
				Summary: map[string]any{
					"openclaw_pid":   snapshot.PID,
					"restarts":       snapshot.Restarts,
					"openclaw_stats": inspect.Stats,
				},
			})
			if err != nil {
				s.logger.Printf("heartbeat failed: %v", err)
				if session.ShouldReRegister(err) {
					_ = s.store.Update(func(st *store.State) {
						st.SessionToken = ""
						st.SessionExpiresAt = time.Time{}
					})
				}
			}
		}
	}
}

func (s *Supervisor) reportLoop(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(s.cfg.StateReportInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.ensureSession(ctx); err != nil {
				errCh <- err
				return
			}
			snapshot := s.process.Snapshot()
			state := s.store.Snapshot()
			inspect := s.inspector.Collect()
			systemInfo := s.profiler.Collect()
			systemInfo["agent"] = protocol.AgentMetadata{
				AgentID:         state.AgentID,
				AgentVersion:    protocol.AgentVersion,
				ProtocolVersion: s.cfg.ProtocolVersion,
				Capabilities:    defaultCapabilities(),
			}
			err := s.client.ReportState(ctx, protocol.StateReportRequest{
				AgentID:    state.AgentID,
				ReportedAt: time.Now().UTC(),
				Agent: protocol.AgentMetadata{
					AgentID:         state.AgentID,
					AgentVersion:    protocol.AgentVersion,
					ProtocolVersion: s.cfg.ProtocolVersion,
					Capabilities:    defaultCapabilities(),
				},
				Runtime: protocol.RuntimePayload{
					OpenClawStatus:          string(snapshot.Status),
					OpenClawPID:             optionalInt(snapshot.PID),
					OpenClawVersion:         inspect.Version,
					CurrentConfigRevisionID: parseOptionalInt(state.CurrentConfigRevisionID),
				},
				SystemInfo: systemInfo,
				Health: map[string]any{
					"status":           snapshot.Status,
					"uptime":           snapshot.Uptime.String(),
					"openclaw_stats":   inspect.Stats,
					"last_exit_reason": snapshot.LastExitReason,
					"last_operation":   snapshot.LastOperation,
					"last_result":      snapshot.LastOperationResult,
				},
			})
			if err != nil {
				s.logger.Printf("report failed: %v", err)
				if session.ShouldReRegister(err) {
					_ = s.store.Update(func(st *store.State) {
						st.SessionToken = ""
						st.SessionExpiresAt = time.Time{}
					})
				}
			}
		}
	}
}

func parseOptionalInt(value string) *int {
	if value == "" {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &n
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func defaultCapabilities() []string {
	return []string{
		"heartbeat",
		"state-report",
		"skill-inventory",
		"skill-installation",
		"skill-risk-control",
		"command-execution",
		"config-apply",
		"process-management",
		"local-debug-http",
	}
}

func (s *Supervisor) skillLoop(ctx context.Context, errCh chan<- error) {
	incrementalTicker := time.NewTicker(s.cfg.SkillIncrementalInterval)
	defer incrementalTicker.Stop()

	fullTicker := time.NewTicker(s.cfg.SkillFullSyncInterval)
	defer fullTicker.Stop()

	if _, err := s.skills.Sync(ctx, "full", "agent_bootstrap", true); err != nil {
		s.logger.Printf("initial skill sync failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-incrementalTicker.C:
			if err := s.ensureSession(ctx); err != nil {
				errCh <- err
				return
			}
			if _, err := s.skills.Sync(ctx, "incremental", "periodic_incremental", false); err != nil {
				s.logger.Printf("incremental skill sync failed: %v", err)
			}
		case <-fullTicker.C:
			if err := s.ensureSession(ctx); err != nil {
				errCh <- err
				return
			}
			if _, err := s.skills.Sync(ctx, "full", "periodic_full", true); err != nil {
				s.logger.Printf("full skill sync failed: %v", err)
			}
		}
	}
}

func (s *Supervisor) commandLoop(ctx context.Context, errCh chan<- error) {
	backoff := s.cfg.CommandPollInterval

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := s.ensureSession(ctx); err != nil {
			errCh <- err
			return
		}

		cmd, err := s.client.NextCommand(ctx)
		if err != nil {
			s.logger.Printf("command poll failed: %v", err)
			if session.ShouldReRegister(err) {
				_ = s.store.Update(func(st *store.State) {
					st.SessionToken = ""
					st.SessionExpiresAt = time.Time{}
				})
			}
			time.Sleep(backoff)
			backoff *= 2
			if backoff > s.cfg.CommandPollBackoffMax {
				backoff = s.cfg.CommandPollBackoffMax
			}
			continue
		}
		backoff = s.cfg.CommandPollInterval

		if cmd == nil || cmd.ID == 0 {
			time.Sleep(s.cfg.CommandPollInterval)
			continue
		}
		if err := s.executor.Execute(ctx, cmd); err != nil {
			s.logger.Printf("command execute failed id=%d type=%s err=%v", cmd.ID, cmd.Type, err)
		}
	}
}

func (s *Supervisor) modelGuardLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			restored, err := s.config.EnforceModelBaseline()
			if err != nil {
				s.logger.Printf("model guard check failed: %v", err)
				continue
			}
			if !restored {
				continue
			}

			s.logger.Printf("detected model field change in openclaw config; restored baseline and restarting openclaw")
			restartCtx, cancel := context.WithTimeout(ctx, s.cfg.ProcessStopTimeout+30*time.Second)
			err = s.process.Restart(restartCtx)
			cancel()
			if err != nil {
				s.logger.Printf("restart openclaw after model restore failed: %v", err)
			}
		}
	}
}
