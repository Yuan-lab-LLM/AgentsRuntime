package hermes

import (
	"errors"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
)

func prepareHermesLiteTeamWorkerHome(workspacePath string, req gateway.CreateGatewayRequest) error {
	root, err := workspaceio.Open(workspacePath)
	if err != nil {
		return err
	}
	defer root.Close()
	realWorkspace, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve Hermes Team instance workspace: %w", err)
	}
	workerHome := filepath.Join(workspacePath, "home", ".clawmanager-team-worker")
	managedDirectories := []string{
		workerHome,
		filepath.Join(workerHome, ".hermes"),
		filepath.Join(workerHome, ".hermes", "runtime"),
		filepath.Join(workerHome, ".cache"),
		filepath.Join(workerHome, ".cache", "npm"),
		filepath.Join(workerHome, ".cache", "uv"),
		filepath.Join(workerHome, ".config"),
		filepath.Join(workerHome, ".local"),
		filepath.Join(workerHome, ".local", "share"),
	}
	for _, directory := range managedDirectories {
		if !gateway.PathWithin(workspacePath, directory) {
			return fmt.Errorf("Hermes Team worker directory escaped the instance workspace: %s", directory)
		}
		if err := root.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create Hermes Team worker directory %s: %w", directory, err)
		}
		info, err := root.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect Hermes Team worker directory %s: %w", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Hermes Team worker path must be a real directory: %s", directory)
		}
		realDirectory, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return fmt.Errorf("resolve Hermes Team worker directory %s: %w", directory, err)
		}
		if !gateway.PathWithin(realWorkspace, realDirectory) {
			return fmt.Errorf("Hermes Team worker directory real path escaped the instance workspace: %s", directory)
		}
		if err := root.Chown(directory, req.UID, req.GID); err != nil {
			return fmt.Errorf("chown Hermes Team worker directory %s: %w", directory, err)
		}
		if err := root.Chmod(directory, 0o750); err != nil {
			return fmt.Errorf("chmod Hermes Team worker directory %s: %w", directory, err)
		}
	}
	readyFile := filepath.Join(workerHome, ".hermes", "runtime", "redis-team.ready.json")
	for _, startupState := range []string{readyFile, readyFile + ".failed"} {
		info, err := root.Lstat(startupState)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect stale Hermes Team startup state %s: %w", startupState, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Hermes Team startup state must be a regular file: %s", startupState)
		}
		if err := root.Remove(startupState); err != nil {
			return fmt.Errorf("remove stale Hermes Team startup state %s: %w", startupState, err)
		}
	}
	return nil
}

func prepareTeamWorkspace(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	if _, _, ok := gateway.LiteTeamEnvironment(req, workspace); !ok {
		return nil
	}
	if err := prepareHermesLiteTeamWorkerHome(workspace, req); err != nil {
		return err
	}
	return gateway.PrepareLiteTeamSharedWorkspace(cfg.WorkspaceRoot, req, workspace, filepath.Join(workspace, "home", ".hermes"), filepath.Join(workspace, "home", ".clawmanager-team-worker", ".hermes"))
}
