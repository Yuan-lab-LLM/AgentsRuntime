package slots

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s sandboxStarter) StartGatewayTerminal(ctx context.Context, spec gateway.GatewayStartSpec, options workbench.TerminalOptions) (workbench.Terminal, error) {
	return s.startGatewayTerminal(ctx, spec, options, nil)
}

func (s sandboxStarter) startGatewayTerminal(ctx context.Context, spec gateway.GatewayStartSpec, options workbench.TerminalOptions, primary *sandbox.Tracker) (workbench.Terminal, error) {
	if filepath.IsAbs(options.Cwd) || strings.ContainsRune(options.Cwd, 0) || strings.Contains(options.Cwd, "\\") {
		return workbench.Terminal{}, workbench.Invalid()
	}
	cwd := filepath.Join(spec.WorkspacePath, options.Cwd)
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || !inside(spec.WorkspacePath, resolved) {
		return workbench.Terminal{}, workbench.Failure("invalid_path", "Terminal directory must be inside the instance workspace.")
	}
	master, slave, err := openTerminal(options.Cols, options.Rows)
	if err != nil {
		return workbench.Terminal{}, err
	}
	spec.WorkingDir = resolved
	binary, err := os.Executable()
	if err != nil {
		master.Close()
		slave.Close()
		return workbench.Terminal{}, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		master.Close()
		slave.Close()
		return workbench.Terminal{}, err
	}
	readyToken := hex.EncodeToString(token[:])
	command := workbench.TerminalCommand
	if primary != nil || s.cfg.SandboxProfile != "nfs-rootless-v1" {
		command = workbench.SharedTerminalCommand
	}
	spec.Command = []string{binary, command, readyToken}
	spec.Env = append(append([]string{}, spec.Env...), "TERM=xterm-256color")
	started := make(chan error, 1)
	done := make(chan error, 1)
	spec.Terminal = &gateway.GatewayTerminalIO{Master: master, ReadyToken: readyToken, Slave: slave, Started: func(err error) { started <- err }}
	go func() {
		_, err := s.runGatewayCommand(ctx, spec, primary)
		slave.Close()
		if errors.Is(err, gateway.ErrGatewayStopFailed) {
			err = errors.Join(gateway.ErrGatewayStopFailed, workbench.Failure("stop_unconfirmed", "Terminal writer teardown is unconfirmed."))
		}
		done <- err
	}()
	if err = <-started; err != nil {
		master.Close()
		cleanupErr := <-done
		if cleanupErr != nil {
			return workbench.Terminal{}, errors.Join(err, cleanupErr)
		}
		return workbench.Terminal{}, err
	}
	return workbench.Terminal{IO: master, Resize: func(cols, rows uint16) error { return resizeTerminal(master, cols, rows) }, Wait: func() error {
		err := <-done
		_ = master.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		return err
	}}, nil
}
