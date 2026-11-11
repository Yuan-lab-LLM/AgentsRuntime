package slots

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
	"syscall"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/workbench"
)

type sandboxStarter struct {
	cfg     gateway.Config
	network *networkOwner
}

func (s sandboxStarter) StartGateway(_ context.Context, spec gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	commandSpec, groups, err := s.businessCommandSpec(spec)
	if err != nil {
		return gateway.ManagedProcess{}, err
	}
	argv, err := sandbox.Command(commandSpec)
	if err != nil {
		return gateway.ManagedProcess{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = configureSandboxProcess(cmd, spec.UID, spec.GID, groups); err != nil {
		return gateway.ManagedProcess{}, err
	}
	track := sandbox.Track
	if s.cfg.SandboxProfile == "nfs-rootless-v1" {
		track = sandbox.TrackContainer
	}
	tracker, err := track(cmd)
	if err != nil {
		return gateway.ManagedProcess{}, err
	}
	var execution *networkSession
	if s.cfg.NetworkSecurityProfile != "" {
		execution, err = s.network.lookup(spec)
		if err != nil {
			tracker.Close()
			return gateway.ManagedProcess{}, err
		}
		if err = execution.wrap(cmd, spec.UID, spec.GID, groups); err != nil {
			tracker.Close()
			return gateway.ManagedProcess{}, err
		}
	}
	if err = cmd.Start(); err != nil {
		tracker.Close()
		return gateway.ManagedProcess{}, err
	}
	kill := func() error {
		if s.cfg.SandboxProfile == "nfs-rootless-v1" {
			return sandbox.SignalProcess(cmd.Process, uint32(spec.UID), syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
	policy := &sandboxStopPolicy{}
	stopNetworkFailure := func() bool { return true }
	if execution != nil {
		stopNetworkFailure = context.AfterFunc(execution.ctx, func() { policy.networkFailure(func() { _ = kill() }) })
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	startErr := tracker.Started(startupCtx)
	cancelStartup()
	if startErr != nil {
		log.Printf("runtime-agent sandbox startup identity failed: %v", startErr)
		tracker.Close()
	}
	done := make(chan error, 1)
	waited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); stopNetworkFailure(); done <- waitErr; close(done); close(waited) }()
	var mu sync.Mutex
	confirmedStop := func(ctx context.Context) error {
		if err := tracker.WaitStopped(ctx); err != nil {
			return err
		}
		return tracker.Close()
	}
	process := gateway.ManagedProcess{PID: cmd.Process.Pid, Done: done, FenceIdleStop: policy.fenceIdle, GracefulStop: newSandboxGracefulStop(&mu, tracker.Terminate, confirmedStop, waited),
		ConfirmStopped: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			select {
			case <-waited:
				return confirmedStop(ctx)
			default:
				return gateway.ErrGatewayStopFailed
			}
		}, VerifyListeners: func(ctx context.Context) error {
			block := s.cfg.GatewayPortBlockSize
			if block < 1 {
				block = 1
			}
			err := tracker.VerifyListeners(ctx, spec.Port, block)
			if err != nil {
				log.Printf("runtime-agent sandbox listener attribution failed: %v", err)
			}
			return err
		}, Stop: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			select {
			case <-waited:
				return confirmedStop(ctx)
			default:
			}
			timeout := s.cfg.ProcessStopTimeout
			if timeout <= 0 {
				timeout = 20 * time.Second
			}
			// Signal the pinned primary gateway so its application shutdown hooks
			// can flush data. Killing the monitor first tears down its namespace.
			signalCtx, cancelSignal := context.WithTimeout(ctx, 2*time.Second)
			signalErr := tracker.Terminate(signalCtx)
			cancelSignal()
			if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
				// Identity/permission failures never cause numeric-PID signaling.
				// Forced teardown below must still prove the whole namespace died.
				if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return err
				}
			}
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			select {
			case <-waited:
				return confirmedStop(ctx)
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				return ctx.Err()
			case <-timer.C:
			}
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
			select {
			case <-waited:
				return confirmedStop(ctx)
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	if s.cfg.SandboxProfile == "nfs-rootless-v1" {
		primary := newContainerPrimaryStop(&mu, tracker.Terminate, kill, waited, s.cfg.ProcessStopTimeout)
		prepare := func(ctx context.Context, graceful bool) error {
			if err := primary(ctx, graceful); err != nil {
				return err
			}
			return tracker.Close()
		}
		process.PrepareReset = prepare
		process.GracefulStop = func(ctx context.Context) error {
			return errors.Join(prepare(ctx, true), sandbox.ErrContainerResetRequired)
		}
		process.Stop = func(ctx context.Context) error {
			return errors.Join(prepare(ctx, false), sandbox.ErrContainerResetRequired)
		}
		process.ConfirmStopped = func(context.Context) error { return sandbox.ErrContainerResetRequired }
	}
	attachWorkbenchProcesses(&process, tracker, spec)
	process.StartTerminal = func(ctx context.Context, options workbench.TerminalOptions) (workbench.Terminal, error) {
		if s.cfg.SandboxProfile == "nfs-rootless-v1" {
			// This profile already shares the slot PID namespace. Retain its
			// existing rootless network attach and private terminal user scope.
			return s.startGatewayTerminal(ctx, spec, options, nil)
		}
		return s.startGatewayTerminal(ctx, spec, options, tracker)
	}
	return process, startErr
}

// businessCommandSpec is shared by the primary gateway and its joined business
// commands: both receive the same narrow mount/group and management-env policy.
func (s sandboxStarter) businessCommandSpec(spec gateway.GatewayStartSpec) (sandbox.Spec, []uint32, error) {
	env := businessEnvironment(spec.Env)
	values := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		values[k] = v
	}
	mounts, groups, err := authorizedSandboxMounts(s.cfg, spec, values)
	if err != nil {
		return sandbox.Spec{}, nil, err
	}
	if s.cfg.ProxyPolicyFile != "" {
		if s.cfg.ProxyPolicyFile != gateway.ManagedProxyPolicyFile {
			return sandbox.Spec{}, nil, fmt.Errorf("unsupported managed policy path")
		}
		mounts = append(mounts, sandbox.Mount{Source: filepath.Dir(s.cfg.ProxyPolicyFile), Target: filepath.Dir(s.cfg.ProxyPolicyFile), ReadOnly: true})
	}
	command := gateway.RuntimeGatewayCommand(s.cfg, spec.Command, env)
	tmpSource, err := prepareInstanceTmp(s.cfg, spec)
	if err != nil {
		return sandbox.Spec{}, nil, err
	}
	commandSpec := sandbox.Spec{ContainerLifecycle: s.cfg.SandboxProfile == "nfs-rootless-v1", Args: command, Env: env, Dir: spec.WorkspacePath, UID: spec.UID, GID: spec.GID, Mounts: mounts, TmpSource: tmpSource}
	if s.cfg.NetworkSecurityProfile != "" {
		session, err := s.network.lookup(spec)
		if err != nil {
			return sandbox.Spec{}, nil, err
		}
		commandSpec.NetworkHostsFile, commandSpec.NetworkResolvConf = session.hosts, session.resolver
		commandSpec.Env = append(commandSpec.Env, "CLAWMANAGER_NETWORK_ISOLATED=1")
	}
	return commandSpec, groups, nil
}

// The trusted request supplies the assignment, but aliases must still fail
// closed: resolving a symlink into another instance would bypass sandbox's
// source-path validation. Configured storage roots must already be canonical.
func checkedSandboxSource(source string) (string, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", fmt.Errorf("sandbox source must be canonical and absolute")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	if resolved != source {
		return "", fmt.Errorf("sandbox source must not contain symlinks")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox source must be a directory")
	}
	return source, nil
}

func authorizedSandboxMounts(cfg gateway.Config, spec gateway.GatewayStartSpec, values map[string]string) ([]sandbox.Mount, []uint32, error) {
	source, err := checkedSandboxSource(spec.WorkspacePath)
	if err != nil {
		return nil, nil, err
	}
	mounts := []sandbox.Mount{{Source: source, Target: spec.WorkspacePath}}
	shared := values["CLAWMANAGER_TEAM_SHARED_DIR"]
	if shared != "" {
		if !inside(spec.WorkspacePath, shared) {
			team := values["CLAWMANAGER_TEAM_ID"]
			if team == "" || team == "." || team == ".." || team != strings.TrimSpace(team) || strings.ContainsAny(team, `/\`) {
				return nil, nil, fmt.Errorf("invalid Team identity")
			}
			expected := filepath.Join(cfg.WorkspaceRoot, "teams", fmt.Sprintf("user-%d", spec.UserID), "team-"+team+"-shared")
			if shared != expected {
				return nil, nil, fmt.Errorf("unauthorized Team mount")
			}
			teamSource, err := checkedSandboxSource(shared)
			if err != nil {
				return nil, nil, err
			}
			mounts = append(mounts, sandbox.Mount{Source: teamSource, Target: shared})
		}
	}
	groups := []uint32{}
	if raw := values["CLAWMANAGER_TEAM_SHARED_GID"]; raw != "" {
		gid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || gid == 0 || gid == 1<<32-1 || shared == "" {
			return nil, nil, fmt.Errorf("invalid Team group")
		}
		if _, err := checkedSandboxSource(shared); err != nil {
			return nil, nil, err
		}
		actual, err := sandboxDirectoryGroup(shared)
		if err != nil || uint32(gid) != actual {
			return nil, nil, fmt.Errorf("Team group does not match authorized directory")
		}
		groups = append(groups, uint32(gid))
	}
	return mounts, groups, nil
}

func businessEnvironment(env []string) []string {
	result := make([]string, 0, len(env))
	positions := map[string]int{}
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "CLAWMANAGER_NETWORK_ISOLATED" || strings.HasPrefix(key, "RUNTIME_AGENT_") || strings.HasPrefix(key, "RUNTIME_SLOT_") || strings.HasPrefix(key, "POD_") || strings.HasPrefix(key, "KUBERNETES_") || (strings.HasPrefix(key, "CLAWMANAGER_AGENT_") && key != "CLAWMANAGER_AGENT_PERSISTENT_DIR") {
			continue
		}
		if i, ok := positions[key]; ok {
			result[i] = entry
		} else {
			positions[key] = len(result)
			result = append(result, entry)
		}
	}
	return result
}

func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
