package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const teamSharedWorkspaceMode fs.FileMode = fs.ModeSetgid | 0o775

func PrepareLiteTeamSharedWorkspace(workspaceRoot string, req CreateGatewayRequest, workspacePath string, aliasParents ...string) error {
	_, sharedDir, ok := LiteTeamEnvironment(req, workspacePath)
	if !ok {
		return nil
	}
	sharedDir = filepath.Clean(filepath.FromSlash(sharedDir))
	if !filepath.IsAbs(sharedDir) {
		return fmt.Errorf("%s must be absolute: %s", teamSharedDirEnv, sharedDir)
	}
	if err := validateLiteTeamSharedPath(workspaceRoot, req, workspacePath, sharedDir); err != nil {
		return err
	}
	teamRoot, err := openLiteTeamRoot(workspaceRoot, req, workspacePath, sharedDir)
	if err != nil {
		return err
	}
	defer teamRoot.Close()
	sharedGID := liteTeamSharedGID(req)
	if err := repairLiteTeamSharedTree(teamRoot, sharedGID); err != nil {
		return err
	}
	instanceRoot, err := workspaceio.Ensure(workspacePath, 0750)
	if err != nil {
		return err
	}
	defer instanceRoot.Close()
	for _, aliasParent := range aliasParents {
		if err := ensureLiteTeamWorkspaceAlias(instanceRoot, aliasParent, sharedDir, req); err != nil {
			return err
		}
	}
	return nil
}

func validateLiteTeamSharedPath(workspaceRoot string, req CreateGatewayRequest, workspacePath, sharedDir string) error {
	workspacePath = filepath.Clean(workspacePath)
	if sharedDir == filepath.Join(workspacePath, "team") {
		return nil
	}
	teamID, ok := requestEnvValue(req, "CLAWMANAGER_TEAM_ID")
	teamID = strings.TrimSpace(teamID)
	if !ok || teamID == "" || strings.ContainsAny(teamID, `/\\`) || teamID == "." || teamID == ".." {
		return fmt.Errorf("valid CLAWMANAGER_TEAM_ID is required for external Team shared workspace")
	}
	root := filepath.Clean(workspaceRoot)
	if root == "." || root == "" {
		root = filepath.Dir(filepath.Dir(filepath.Dir(workspacePath)))
	}
	expected := filepath.Join(root, "teams", "user-"+strconv.Itoa(req.UserID), "team-"+teamID+"-shared")
	if sharedDir != filepath.Clean(expected) {
		return fmt.Errorf("Team shared workspace escaped current Team scope: got %s want %s", sharedDir, expected)
	}
	return nil
}

func repairLiteTeamSharedTree(root *workspaceio.FS, gid int) error {
	return fs.WalkDir(root.Root.FS(), ".", func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if err := root.Chgrp(filepath.FromSlash(current), gid); err != nil {
			return fmt.Errorf("set Team shared group on %s: %w", current, err)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			mode := teamSharedWorkspaceMode | info.Mode().Perm() | 0o770
			if err := root.Chmod(filepath.FromSlash(current), mode); err != nil {
				return fmt.Errorf("repair Team shared directory %s: %w", current, err)
			}
			return nil
		}
		mode := info.Mode().Perm() | 0o660
		if err := root.Chmod(filepath.FromSlash(current), mode); err != nil {
			return fmt.Errorf("repair Team shared file %s: %w", current, err)
		}
		return nil
	})
}

func ensureLiteTeamWorkspaceAlias(root *workspaceio.FS, aliasParent, sharedDir string, req CreateGatewayRequest) error {
	if err := root.MkdirAll(aliasParent, 0o750); err != nil {
		return fmt.Errorf("create Team workspace alias parent: %w", err)
	}
	if err := root.Chown(aliasParent, req.UID, req.GID); err != nil {
		return fmt.Errorf("chown Team workspace alias parent: %w", err)
	}
	alias := filepath.Join(aliasParent, "team")
	info, err := root.Lstat(alias)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := root.Readlink(alias)
			if readErr != nil {
				return readErr
			}
			if filepath.Clean(target) != sharedDir {
				return fmt.Errorf("Team workspace alias already points to a different Team: %s", target)
			}
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("Team workspace alias path is not a directory or symlink: %s", alias)
		}
		entries, readErr := root.ReadDir(alias)
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			if removeErr := root.Remove(alias); removeErr != nil {
				return removeErr
			}
		} else {
			memberID, _ := requestEnvValue(req, "CLAWMANAGER_TEAM_MEMBER_ID")
			backup := alias + ".private-backup-" + safeWorkspaceComponent(memberID)
			if _, backupErr := root.Lstat(backup); backupErr == nil {
				return fmt.Errorf("Team private workspace backup already exists: %s", backup)
			} else if !os.IsNotExist(backupErr) {
				return backupErr
			}
			if renameErr := root.Rename(alias, backup); renameErr != nil {
				return fmt.Errorf("preserve existing private Team workspace: %w", renameErr)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := root.Symlink(sharedDir, alias); err != nil {
		return fmt.Errorf("create per-instance Team workspace alias: %w", err)
	}
	return nil
}

func safeWorkspaceComponent(value string) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "member"
	}
	return out.String()
}

func openLiteTeamRoot(workspaceRoot string, req CreateGatewayRequest, workspace, shared string) (*workspaceio.FS, error) {
	if err := validateLiteTeamSharedPath(workspaceRoot, req, workspace, shared); err != nil {
		return nil, err
	}
	if filepath.Clean(shared) == filepath.Join(filepath.Clean(workspace), "team") {
		root, err := workspaceio.Ensure(workspace, 0750)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return root.Child("team", teamSharedWorkspaceMode)
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = filepath.Dir(filepath.Dir(filepath.Dir(workspace)))
	}
	return workspaceio.OpenOrCreateAt(workspaceRoot, shared, teamSharedWorkspaceMode)
}
