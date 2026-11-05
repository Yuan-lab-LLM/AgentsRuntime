package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"github.com/iamlovingit/clawmanager-agent/workspacegit"
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
func (m *GatewayManager) WorkbenchWithAdmission(ctx context.Context, req workbench.Request, release func()) (result any, resultErr error) {
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
	record.activeCommands++
	m.mu.Unlock()
	unlock()
	defer func() {
		m.mu.Lock()
		record.activeCommands--
		if errors.Is(resultErr, ErrGatewayStopFailed) {
			record.automationStopFailed = true
		}
		m.mu.Unlock()
		attempt.requests.Done()
	}()
	joined, cancel := context.WithTimeout(ctx, 2*time.Minute)
	stop := context.AfterFunc(attempt.ctx, cancel)
	defer func() { stop(); cancel() }()
	ctx = joined
	if process.VerifyListeners == nil {
		return nil, workbench.Failure("unsupported_runtime", "Runtime ownership verification is unavailable.")
	}
	if err := process.VerifyListeners(ctx); err != nil {
		return nil, ErrGatewayIdleBusy
	}
	switch req.Operation {
	case "capabilities":
		_, terminal := m.starter.(workbenchTerminalStarter)
		_, git := m.starter.(GatewayCommandRunner)
		return map[string]any{"git": git, "terminal": terminal, "preview": false, "processes": true, "version": 1}, nil
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
