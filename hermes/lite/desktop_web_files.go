package hermes

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const desktopWebConfigMaxBytes = 4 << 20

// openDesktopWebHome opens each directory relative to an already-open parent.
// os.Root keeps subsequent operations inside this instance even if a user
// concurrently changes a symlink. Managed directories themselves cannot be links.
func openDesktopWebHome(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) (*os.Root, error) {
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return nil, fmt.Errorf("workspace_unavailable: invalid Hermes workspace path")
	}
	runtimeType := cfg.RuntimeType
	if runtimeType == "" {
		runtimeType = "hermes"
	}
	if runtimeType != "hermes" {
		return nil, fmt.Errorf("workspace_unavailable: Hermes Desktop Web requires the Hermes Lite runtime")
	}
	rootPath := cfg.WorkspaceRoot
	if rootPath == "" {
		rootPath = filepath.Dir(filepath.Dir(filepath.Dir(workspace)))
	}
	parts := []string{runtimeType, "user-" + strconv.Itoa(req.UserID), "instance-" + strconv.Itoa(req.InstanceID), "home", ".hermes"}
	expected := filepath.Join(rootPath, parts[0], parts[1], parts[2])
	if workspace != expected || (req.WorkspacePath != "" && req.WorkspacePath != expected) {
		return nil, fmt.Errorf("workspace_unavailable: Hermes workspace does not match the requested instance")
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, fmt.Errorf("workspace_unavailable: create Hermes workspace root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: open Hermes workspace root: %w", err)
	}
	for index, part := range parts {
		mode := os.FileMode(0o755)
		if index >= 2 {
			mode = 0o750
		}
		mkdirErr := root.Mkdir(part, mode)
		if mkdirErr != nil && !os.IsExist(mkdirErr) {
			root.Close()
			return nil, fmt.Errorf("workspace_unavailable: create Hermes workspace directory: %w", mkdirErr)
		}
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("workspace_unavailable: Hermes workspace directory must not be a symlink")
		}
		next, err := root.OpenRoot(part)
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("workspace_unavailable: open Hermes workspace directory: %w", err)
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, fmt.Errorf("workspace_unavailable: Hermes workspace directory changed during preparation")
		}
		root = next
		dir, err := root.Open(".")
		if err == nil {
			if index >= 2 {
				err = setDesktopWebFileOwner(dir, req.UID, req.GID)
			} else if mkdirErr != nil {
				// Existing shared parents may deliberately be 0711. Preserve
				// their owner and listing permissions; only ensure traversal.
				mode = opened.Mode() | 0o111
			}
			if err == nil && (index >= 2 || mkdirErr == nil || mode != opened.Mode()) {
				// The Lite entrypoint uses umask 077. Mkdir(0755) alone
				// leaves fresh shared parents inaccessible to instance UIDs.
				err = dir.Chmod(mode)
			}
			dir.Close()
		}
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("workspace_unavailable: set Hermes directory permissions: %w", err)
		}
	}
	return root, nil
}

func setDesktopWebFileOwner(file *os.File, uid, gid int) error {
	if runtime.GOOS == "windows" || uid <= 0 || gid <= 0 {
		return nil
	}
	return file.Chown(uid, gid)
}

func readDesktopWebFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: inspect Hermes %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace_unavailable: Hermes %s must be a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: open Hermes %s: %w", name, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("workspace_unavailable: Hermes %s changed during preparation", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, desktopWebConfigMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: read Hermes %s: %w", name, err)
	}
	if len(data) > desktopWebConfigMaxBytes {
		return nil, fmt.Errorf("workspace_unavailable: Hermes %s exceeds the configuration size limit", name)
	}
	return data, nil
}

func writeDesktopWebFile(root *os.Root, name string, data []byte, uid, gid int) error {
	if strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("workspace_unavailable: invalid managed Hermes file name")
	}
	if _, err := readDesktopWebFile(root, name); err != nil {
		return err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("workspace_unavailable: allocate managed Hermes temporary file: %w", err)
	}
	tmpName := "." + name + "." + hex.EncodeToString(random[:]) + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("workspace_unavailable: create managed Hermes temporary file: %w", err)
	}
	defer root.Remove(tmpName)
	defer tmp.Close()
	if err := setDesktopWebFileOwner(tmp, uid, gid); err != nil {
		return fmt.Errorf("workspace_unavailable: set managed Hermes file ownership: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("workspace_unavailable: set managed Hermes file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("workspace_unavailable: write managed Hermes file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("workspace_unavailable: sync managed Hermes file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("workspace_unavailable: close managed Hermes file: %w", err)
	}
	if err := root.Rename(tmpName, name); err != nil {
		return fmt.Errorf("workspace_unavailable: replace managed Hermes file: %w", err)
	}
	return nil
}
