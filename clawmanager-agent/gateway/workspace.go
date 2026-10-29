package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func ValidateWorkspacePath(root, runtimeType string, req CreateGatewayRequest) (string, error) {
	root = filepath.Clean(root)
	expected := filepath.Join(
		root,
		runtimeType,
		"user-"+strconv.Itoa(req.UserID),
		"instance-"+strconv.Itoa(req.InstanceID),
	)
	requested := filepath.Clean(req.WorkspacePath)
	if requested != expected {
		return "", fmt.Errorf("%w: got %s want %s", ErrWorkspacePath, requested, expected)
	}
	if !pathWithin(root, requested) {
		return "", fmt.Errorf("%w: %s is outside %s", ErrWorkspacePath, requested, root)
	}
	return requested, nil
}

func PrepareWorkspace(root, runtimeType string, req CreateGatewayRequest) (string, error) {
	workspace, err := ValidateWorkspacePath(root, runtimeType, req)
	if err != nil {
		return "", err
	}
	// The pool root is administrator-owned; every descendant is pinned before
	// privileged permissions or ownership changes are made.
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	pool, err := workspaceio.Open(root)
	if err != nil {
		return "", err
	}
	defer pool.Close()
	runtime, err := pool.Child(runtimeType, 0755)
	if err != nil {
		return "", err
	}
	defer runtime.Close()
	if err := runtime.Chmod(".", 0755); err != nil {
		return "", err
	}
	user, err := runtime.Child("user-"+strconv.Itoa(req.UserID), 0755)
	if err != nil {
		return "", err
	}
	defer user.Close()
	if err := user.Chmod(".", 0755); err != nil {
		return "", err
	}
	instance, err := user.Child("instance-"+strconv.Itoa(req.InstanceID), 0750)
	if err != nil {
		return "", err
	}
	defer instance.Close()
	if err := instance.Chown(".", req.UID, req.GID); err != nil {
		return "", err
	}
	home, err := instance.Child("home", 0750)
	if err != nil {
		return "", err
	}
	defer home.Close()
	if err := home.Chown(".", req.UID, req.GID); err != nil {
		return "", err
	}
	return workspace, nil
}

func pathWithin(root, child string) bool {
	root = filepath.Clean(root)
	child = filepath.Clean(child)
	if root == child {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func PathWithin(root, child string) bool { return pathWithin(root, child) }
