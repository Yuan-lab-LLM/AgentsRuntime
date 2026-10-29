package openclaw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/slots"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type readOnlyUpgradeCommandKey struct{}

func withReadOnlyUpgradeCommands(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyUpgradeCommandKey{}, true)
}

func (m *upgradeService) combinedUpgradeOutput(ctx context.Context, cmd *exec.Cmd, req WorkspaceUpgradeRequest) ([]byte, error) {
	if !m.cfg.IsolatedLifecycle {
		return cmd.CombinedOutput()
	}
	var output limitedUpgradeOutput
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, fmt.Errorf("combined upgrade command already has output writers")
	}
	cmd.Stdout, cmd.Stderr = &output, &output
	err := m.runUpgradeCommand(ctx, cmd, req)
	return output.Bytes(), err
}

type limitedUpgradeOutput struct{ bytes.Buffer }

func (b *limitedUpgradeOutput) Write(p []byte) (int, error) {
	count := len(p)
	remaining := (64 << 10) - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return count, nil
}

func (m *upgradeService) runUpgradeCommand(ctx context.Context, original *exec.Cmd, req WorkspaceUpgradeRequest) error {
	readOnly, _ := ctx.Value(readOnlyUpgradeCommandKey{}).(bool)
	if m.cfg.NetworkSecurityProfile != "" {
		maintenanceCtx, finish, err := m.protectedMaintenanceContext(ctx, req, readOnly)
		if err != nil {
			return err
		}
		defer finish()
		ctx = maintenanceCtx
	}
	if !m.cfg.IsolatedLifecycle {
		return original.Run()
	}
	spec, err := m.upgradeSandboxSpec(original, req, readOnly)
	if err != nil {
		return err
	}
	finish := func(bool) error { return nil }
	if !readOnly && m.io.writerFinish == nil {
		finish, err = slots.AcquireStoppedCommandWriter(m.cfg, req)
		if err != nil {
			return err
		}
	}
	confirmed := false
	defer func() {
		if !confirmed && m.io != nil {
			m.io.commandUncertain = true
		}
		_ = finish(confirmed)
	}()
	argv, err := sandbox.Command(spec)
	if err != nil {
		confirmed = true
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = original.Stdin, original.Stdout, original.Stderr
	configureUpgradeSandboxProcess(cmd, req.UID, req.GID)
	tracker, err := sandbox.Track(cmd)
	if err != nil {
		confirmed = true
		return err
	}
	defer tracker.Close()
	if err = cmd.Start(); err != nil {
		confirmed = true
		return err
	}
	startedCtx, cancelStart := context.WithTimeout(ctx, 10*time.Second)
	startErr := tracker.Started(startedCtx)
	cancelStart()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	commandErr := startErr
	exited := false
	if startErr != nil {
		_ = cmd.Process.Kill()
	} else {
		select {
		case commandErr = <-waited:
			exited = true
		case <-ctx.Done():
			commandErr = ctx.Err()
		}
		if !exited {
			signalCtx, cancelSignal := context.WithTimeout(context.Background(), 2*time.Second)
			_ = tracker.Terminate(signalCtx)
			cancelSignal()
		}
	}
	grace := m.cfg.ProcessStopTimeout
	if grace <= 0 {
		grace = 20 * time.Second
	}
	if !exited {
		timer := time.NewTimer(grace)
		select {
		case <-waited:
		case <-timer.C:
			_ = cmd.Process.Kill()
			// Keep both the output writers and operation fence alive until Wait
			// has joined all child-output copies; uncertain writers stay blocked.
			<-waited
		}
		timer.Stop()
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), grace+5*time.Second)
	defer cancelStop()
	if err = tracker.WaitStopped(stopCtx); err != nil {
		return errors.Join(commandErr, fmt.Errorf("upgrade sandbox teardown is unconfirmed: %w", err))
	}
	confirmed = true
	if err = finish(true); err != nil {
		return errors.Join(commandErr, err)
	}
	return commandErr
}

func (m *upgradeService) upgradeSandboxSpec(cmd *exec.Cmd, req WorkspaceUpgradeRequest, readOnly bool) (sandbox.Spec, error) {
	if req.UID <= 0 || req.GID <= 0 || cmd == nil || len(cmd.Args) == 0 || len(cmd.ExtraFiles) != 0 || m.io == nil || m.cfg.GatewayBaseEnv == nil {
		return sandbox.Spec{}, fmt.Errorf("upgrade sandbox requires assigned identity, safe environment, bounded IO and no management descriptors")
	}
	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return sandbox.Spec{}, err
	}
	dir := cmd.Dir
	if dir == "" {
		dir = workspace
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return sandbox.Spec{}, fmt.Errorf("invalid upgrade command directory")
	}
	mounts := []sandbox.Mount{{Source: workspace, Target: workspace, ReadOnly: readOnly}}
	control := m.io.controlRoot()
	rollout := req.RolloutID
	if rollout == "" {
		rollout = "preflight"
	}
	expectedControl := filepath.Join(m.host.UpgradeRoot(), "rollout-"+rollout, fmt.Sprintf("instance-%d", req.InstanceID))
	extra := map[string]bool{}
	addPath := func(value string) error {
		if !filepath.IsAbs(value) || gateway.PathWithin(workspace, value) {
			return nil
		}
		if control == expectedControl && gateway.PathWithin(control, value) && value != control {
			rel, err := filepath.Rel(control, value)
			if err != nil {
				return err
			}
			root := filepath.Join(control, strings.Split(rel, string(filepath.Separator))[0])
			info, err := os.Lstat(root)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("upgrade capsule command path is not a private directory")
			}
			extra[root] = true
			return nil
		}
		// A configured pool root can itself live under /tmp or /opt. Its other
		// instances and management paths never inherit those general allowances.
		if gateway.PathWithin(m.cfg.WorkspaceRoot, value) {
			return fmt.Errorf("upgrade command references another workspace or management scope")
		}
		for _, base := range []string{"/usr", "/bin", "/sbin", "/opt", "/defaults", "/tmp"} {
			if gateway.PathWithin(base, value) {
				return nil
			}
		}
		return fmt.Errorf("upgrade command references a path outside its assigned scopes")
	}
	if err = addPath(dir); err != nil {
		return sandbox.Spec{}, err
	}
	for _, arg := range cmd.Args[1:] {
		if err = addPath(arg); err != nil {
			return sandbox.Spec{}, err
		}
	}
	for _, item := range cmd.Env {
		key, value, _ := strings.Cut(item, "=")
		if key == "CLAWMANAGER_OPENCLAW_UPGRADE_PLUGIN_CAPTURE_ROOT" {
			allowed := false
			for _, scratch := range m.io.scratch {
				if value == scratch {
					allowed = true
					break
				}
			}
			if !allowed || filepath.Dir(value) != "/tmp" || !strings.HasPrefix(filepath.Base(value), "clawmanager-upgrade-plugin-") {
				return sandbox.Spec{}, fmt.Errorf("upgrade scratch is not owned by this operation")
			}
			extra[value] = true
		} else if key == "HOME" || key == "OPENCLAW_STATE_DIR" || key == "OPENCLAW_CONFIG_PATH" || key == "TMPDIR" || key == "XDG_CACHE_HOME" || key == "XDG_STATE_HOME" {
			if err = addPath(value); err != nil {
				return sandbox.Spec{}, err
			}
		}
	}
	for root := range extra {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return sandbox.Spec{}, fmt.Errorf("upgrade mount contains a symbolic link")
		}
		if gateway.PathWithin(control, root) {
			if err = workspaceio.AllowManagementTraversal(m.cfg.WorkspaceRoot, control); err != nil {
				return sandbox.Spec{}, err
			}
			if err = m.chownTree(root, req.UID, req.GID); err != nil {
				return sandbox.Spec{}, err
			}
		}
		mounts = append(mounts, sandbox.Mount{Source: root, Target: root})
	}
	return sandbox.Spec{OfflineNetwork: m.cfg.NetworkSecurityProfile != "", Args: append([]string(nil), cmd.Args...), Env: append([]string(nil), cmd.Env...), Dir: dir, UID: req.UID, GID: req.GID, Mounts: mounts}, nil
}
