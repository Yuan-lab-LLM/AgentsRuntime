package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"github.com/iamlovingit/clawmanager-agent/workspacegit"
	"net"
	"os"
	"sync"
	"time"
)

// An attempt owns its requests and sessions. Admission and idle commitment share
// m.mu; sessions do not count as active commands after creation returns.
type workbenchAttempt struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests sync.WaitGroup
	sessions *workbench.Sessions
}
type workbenchTerminalStarter interface {
	StartGatewayTerminal(context.Context, GatewayStartSpec, workbench.TerminalOptions) (workbench.Terminal, error)
}

func (m *GatewayManager) Workbench(ctx context.Context, req workbench.Request) (any, error) {
	return m.WorkbenchWithAdmission(ctx, req, nil)
}

// WorkbenchWithAdmission releases an external assignment fence only after the
// manager has admitted or rejected the request. The worker uses this to close
// the durable StopIntent/admission race without locking during business work.
type workbenchPreviewStarter interface {
	GatewayPreviewAvailable(GatewayStartSpec) bool
	DialGatewayPreview(context.Context, GatewayStartSpec, string, string) (net.Conn, error)
}
type previewHandler func(context.Context, func(context.Context, string, string) (net.Conn, error)) error

func (m *GatewayManager) WorkbenchPreviewWithAdmission(ctx context.Context, req workbench.Request, release func(), serve previewHandler) error {
	_, err := m.workbenchWithAdmission(ctx, req, release, serve, true)
	return err
}
func (m *GatewayManager) WorkbenchWithAdmission(ctx context.Context, req workbench.Request, release func()) (any, error) {
	return m.workbenchWithAdmission(ctx, req, release, nil, false)
}
func (m *GatewayManager) workbenchWithAdmission(ctx context.Context, req workbench.Request, release func(), serve previewHandler, preview bool) (result any, resultErr error) {
	var releaseOnce sync.Once
	unlock := func() {
		if release != nil {
			releaseOnce.Do(release)
		}
	}
	defer unlock()

	m.mu.Lock()
	record := m.gateways[req.GatewayID]
	if req.InstanceID <= 0 || req.UserID <= 0 || req.Generation <= 0 || record == nil || record.state.InstanceID != req.InstanceID || record.state.UserID != req.UserID || record.state.Generation != req.Generation {
		m.mu.Unlock()
		return nil, ErrStaleGeneration
	}
	if !m.IsolatedGatewayLifecycle() || record.state.State != "running" || record.process.PID <= 0 || record.deletePending || record.idleStop || record.idleChecking || record.stopOperation != "" || record.automationStopFailed || m.draining {
		m.mu.Unlock()
		return nil, ErrGatewayIdleBusy
	}
	if record.workbench == nil {
		lifetime, cancel := context.WithCancel(context.Background())
		record.workbench = &workbenchAttempt{ctx: lifetime, cancel: cancel}
		record.workbench.sessions = workbench.NewSessions(lifetime, req.Generation)
	}
	attempt := record.workbench
	spec, process := record.startSpec, record.process
	if attempt.ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ErrGatewayIdleBusy
	}
	attempt.requests.Add(1)
	if !preview {
		record.activeCommands++
	}
	m.mu.Unlock()
	unlock()
	defer func() {
		m.mu.Lock()
		if !preview {
			record.activeCommands--
		}
		if errors.Is(resultErr, ErrGatewayStopFailed) {
			record.automationStopFailed = true
		}
		m.mu.Unlock()
		attempt.requests.Done()
	}()
	joined, cancel := context.WithCancel(ctx)
	if !preview {
		cancel()
		joined, cancel = context.WithTimeout(ctx, 2*time.Minute)
	}
	stop := context.AfterFunc(attempt.ctx, cancel)
	defer func() { stop(); cancel() }()
	ctx = joined
	if process.VerifyListeners == nil {
		return nil, workbench.Failure("unsupported_runtime", "Runtime ownership verification is unavailable.")
	}
	if err := process.VerifyListeners(ctx); err != nil {
		return nil, ErrGatewayIdleBusy
	}
	if preview {
		runner, ok := m.starter.(workbenchPreviewStarter)
		if !ok || !runner.GatewayPreviewAvailable(spec) || serve == nil {
			return nil, workbench.Failure("unsupported_runtime", "Private preview transport is unavailable.")
		}
		return nil, serve(ctx, func(dialCtx context.Context, network, address string) (net.Conn, error) {
			joined, cancel := context.WithCancel(dialCtx)
			stop := context.AfterFunc(ctx, cancel)
			conn, err := runner.DialGatewayPreview(joined, spec, network, address)
			if err != nil {
				stop()
				cancel()
				return nil, err
			}
			// Transport may detach dial cancellation. The admitted request lifetime is
			// independently attached until the actual raw connection closes.
			wrapped := &previewConn{Conn: conn, finish: func() { stop(); cancel() }}
			stopConn := context.AfterFunc(ctx, func() { conn.Close() })
			wrapped.finish = func() { stopConn(); stop(); cancel() }
			return wrapped, nil
		})
	}
	switch req.Operation {
	case "capabilities":
		_, terminal := m.starter.(workbenchTerminalStarter)
		_, git := m.starter.(GatewayCommandRunner)
		previewRunner, ok := m.starter.(workbenchPreviewStarter)
		available := ok && previewRunner.GatewayPreviewAvailable(spec)
		return map[string]any{"git": git, "terminal": terminal, "preview": available, "processes": true, "version": 1}, nil
	case "git":
		var request workspacegit.Request
		if json.Unmarshal(req.Data, &request) != nil {
			return nil, workbench.Invalid()
		}
		runner, ok := m.starter.(GatewayCommandRunner)
		if !ok {
			return nil, workbench.Failure("unsupported_runtime", "Business command runner is unavailable.")
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		spec.Command = []string{binary, workbench.GitCommand}
		spec.Stdin, err = json.Marshal(workbench.GitPayload{Root: spec.WorkspacePath, Request: request})
		if err != nil || len(spec.Stdin) > workbench.MaxRequest {
			return nil, workbench.Invalid()
		}
		spec.OutputLimit = workbench.MaxOutput
		raw, err := runner.RunGatewayCommand(ctx, spec)
		if err != nil {
			return nil, err
		}
		var response workbench.GitEnvelope
		if json.Unmarshal(raw, &response) != nil {
			return nil, workbench.Failure("invalid_response", "Git helper returned an invalid response.")
		}
		if response.Error != nil {
			return nil, response.Error
		}
		if response.Result == nil {
			return nil, workbench.Failure("invalid_response", "Git helper returned no result.")
		}
		return response.Result, nil
	default:
		launch := func(lifetime context.Context, options workbench.TerminalOptions) (workbench.Terminal, error) {
			if process.StartTerminal != nil {
				return process.StartTerminal(lifetime, options)
			}
			runner, ok := m.starter.(workbenchTerminalStarter)
			if !ok {
				return workbench.Terminal{}, workbench.Failure("unsupported_runtime", "Terminal execution is unavailable.")
			}
			return runner.StartGatewayTerminal(lifetime, spec, options)
		}
		return attempt.sessions.Execute(ctx, req.Operation, req.Data, launch, process.WorkbenchProcesses, process.WorkbenchStopProcess)
	}
}

// Called with admission fenced, before primary shutdown and any lease release.
func (m *GatewayManager) closeWorkbench(record *gatewayRecord) error {
	m.mu.Lock()
	attempt := record.workbench
	if attempt != nil {
		attempt.cancel()
	}
	m.mu.Unlock()
	if attempt == nil {
		return nil
	}
	attempt.requests.Wait()
	err := attempt.sessions.Close()
	m.mu.Lock()
	if err != nil {
		record.automationStopFailed = true
	} else if record.workbench == attempt {
		record.workbench = nil
	}
	m.mu.Unlock()
	if err != nil {
		return errors.Join(ErrGatewayStopFailed, err)
	}
	return nil
}

// Closing a connection releases the cancellation registration exactly once.
type previewConn struct {
	net.Conn
	once   sync.Once
	finish func()
}

func (c *previewConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.finish() })
	return err
}
