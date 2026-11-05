package slots

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

// RunGatewayCommand uses the active worker's writer lease. It never acquires a
// stopped-workspace lease: the primary gateway is still running. Its caller must
// join this command before releasing that lease or acknowledging gateway stop.
func (s sandboxStarter) RunGatewayCommand(ctx context.Context, spec gateway.GatewayStartSpec) (result []byte, resultErr error) {
	notified := false
	if spec.Terminal != nil {
		defer func() {
			if !notified {
				spec.Terminal.Started(resultErr)
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.cfg.IsolatedLifecycle || s.cfg.GatewayBaseEnv == nil || spec.UID <= 0 || spec.GID <= 0 || spec.Generation <= 0 {
		return nil, fmt.Errorf("business command requires isolated lifecycle, safe environment and assigned identity")
	}
	workspace, err := gateway.ValidateWorkspacePath(s.cfg.WorkspaceRoot, s.cfg.RuntimeType, gateway.CreateGatewayRequest{InstanceID: spec.InstanceID, UserID: spec.UserID, WorkspacePath: spec.WorkspacePath})
	if err != nil || workspace != spec.WorkspacePath {
		return nil, fmt.Errorf("business command workspace does not match assignment")
	}
	var execution *networkSession
	if s.cfg.NetworkSecurityProfile != "" {
		var finish func(error)
		execution, finish, err = s.network.join(spec)
		if err != nil {
			return nil, err
		}
		defer func() { finish(resultErr) }()
		joined, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(execution.ctx, cancel)
		defer func() { stop(); cancel() }()
		ctx = joined
	}
	commandSpec, groups, err := s.businessCommandSpec(spec)
	if err != nil {
		return nil, err
	}
	if spec.WorkingDir != "" {
		if !inside(spec.WorkspacePath, spec.WorkingDir) {
			return nil, fmt.Errorf("working directory escapes workspace")
		}
		commandSpec.Dir = spec.WorkingDir
	}
	argv, err := sandbox.Command(commandSpec)
	if err != nil {
		return nil, err
	}
	if spec.Terminal != nil {
		for i, arg := range argv {
			if arg == "--new-session" {
				argv = append(argv[:i], argv[i+1:]...)
				break
			}
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	if s.cfg.SandboxProfile == "nfs-rootless-v1" {
		cmd.WaitDelay = s.cfg.ProcessStopTimeout
		if cmd.WaitDelay <= 0 {
			cmd.WaitDelay = 20 * time.Second
		}
	}
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	var output boundedCommandOutput
	if spec.OutputLimit > 0 && spec.OutputLimit <= 2<<20 {
		output.stdout.limit = spec.OutputLimit
	}
	cmd.Stdout, cmd.Stderr = output.Stdout(), output.Stderr()
	if err = configureSandboxProcess(cmd, spec.UID, spec.GID, groups); err != nil {
		return nil, err
	}
	track := sandbox.Track
	if s.cfg.SandboxProfile == "nfs-rootless-v1" {
		track = sandbox.TrackContainer
	}
	tracker, err := track(cmd)
	if err != nil {
		return nil, err
	}
	defer tracker.Close()
	if execution != nil {
		if err = execution.wrap(cmd, spec.UID, spec.GID, groups); err != nil {
			return nil, err
		}
	}
	if spec.Terminal != nil {
		cmd.Stdin = spec.Terminal.Slave
		cmd.Stdout = spec.Terminal.Slave
		cmd.Stderr = spec.Terminal.Slave
		configureTerminal(cmd)
	}
	// Preparation/identity checks can take time; cancellation must also be checked
	// immediately before spawn. Any concurrent cancellation after Start is joined.
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	kill := func() error {
		if s.cfg.SandboxProfile == "nfs-rootless-v1" {
			return sandbox.SignalProcess(cmd.Process, uint32(spec.UID), syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
	startedCtx, cancelStarted := context.WithTimeout(ctx, 10*time.Second)
	commandErr := tracker.Started(startedCtx)
	cancelStarted()
	if commandErr == nil && spec.Terminal != nil {
		master := spec.Terminal.Master
		_ = master.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, readyErr := bufio.NewReaderSize(io.LimitReader(master, 128), 128).ReadString('\n')
		_ = master.SetReadDeadline(time.Time{})
		if readyErr != nil || strings.TrimSpace(line) != spec.Terminal.ReadyToken {
			commandErr = fmt.Errorf("terminal readiness failed")
		} else if err := tracker.PinWorkbenchNamespace(); err != nil {
			commandErr = err
		} else {
			_, commandErr = master.Write([]byte("\n"))
		}
	}
	if spec.Terminal != nil {
		spec.Terminal.Slave.Close()
		notified = true
		spec.Terminal.Started(commandErr)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	exited := false
	if commandErr == nil {
		select {
		case commandErr = <-waited:
			exited = true
		case <-ctx.Done():
			commandErr = ctx.Err()
		}
	}
	grace := s.cfg.ProcessStopTimeout
	if grace <= 0 {
		grace = 20 * time.Second
	}
	if !exited {
		signalCtx, cancelSignal := context.WithTimeout(context.Background(), 2*time.Second)
		signalErr := tracker.Terminate(signalCtx)
		cancelSignal()
		if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
			_ = kill()
		}
		timer := time.NewTimer(grace)
		select {
		case <-waited:
		case <-timer.C:
			_ = kill()
			// Wait joins inherited output copies as well as the outer process. The
			// lifecycle fence remains held even if a stuck writer delays this join.
			<-waited
		}
		timer.Stop()
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), grace+5*time.Second)
	var stopErr error
	if s.cfg.SandboxProfile != "nfs-rootless-v1" {
		stopErr = tracker.WaitStopped(stopCtx)
	} else if spec.Terminal != nil {
		stopErr = tracker.StopWorkbenchScope(stopCtx, uint32(spec.UID))
	}
	cancelStop()
	if stopErr != nil {
		return output.Result(errors.Join(commandErr, fmt.Errorf("%w: auxiliary command namespace teardown is unconfirmed: %v", gateway.ErrGatewayStopFailed, stopErr)))
	}
	return output.Result(commandErr)
}

const commandOutputLimit = 64 << 10

// Keep stderr separate so successful --json stdout remains parseable. Both
// streams are bounded independently; failures retain stderr's final diagnostic.
type boundedCommandOutput struct{ stdout, stderr boundedCommandStream }

func (b *boundedCommandOutput) Stdout() io.Writer { return &b.stdout }
func (b *boundedCommandOutput) Stderr() io.Writer { b.stderr.tail = true; return &b.stderr }
func (b *boundedCommandOutput) Result(err error) ([]byte, error) {
	if err == nil && b.stdout.truncated {
		err = fmt.Errorf("business command stdout exceeded %d-byte limit", b.stdout.max())
	}
	stdout, stderr := b.stdout.buffer.Bytes(), b.stderr.buffer.Bytes()
	if err == nil || len(stderr) == 0 {
		return stdout, err
	}
	if len(stdout) == 0 {
		return stderr, err
	}
	stderrN := min(len(stderr), commandOutputLimit/2)
	stdoutN := min(len(stdout), commandOutputLimit-stderrN-1)
	stderrN = min(len(stderr), commandOutputLimit-stdoutN-1)
	result := make([]byte, 0, stderrN+1+stdoutN)
	// Diagnostics precede stdout on errors, which downstream callers may shorten.
	result = append(result, stderr[len(stderr)-stderrN:]...)
	result = append(result, '\n')
	result = append(result, stdout[:stdoutN]...)
	return result, err
}

type boundedCommandStream struct {
	mu              sync.Mutex
	buffer          bytes.Buffer
	tail, truncated bool
	limit           int
}

func (b *boundedCommandStream) max() int {
	if b.limit > 0 {
		return b.limit
	}
	return commandOutputLimit
}

func (b *boundedCommandStream) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	limit := b.max()
	remaining := limit - b.buffer.Len()
	if n > remaining {
		b.truncated = true
	}
	if b.tail {
		if n >= limit {
			b.buffer.Reset()
			p = p[n-limit:]
		} else if n > remaining {
			b.buffer.Next(n - remaining)
		}
		_, _ = b.buffer.Write(p)
	} else if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}
