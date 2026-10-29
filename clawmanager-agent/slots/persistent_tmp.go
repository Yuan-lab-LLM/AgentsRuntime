package slots

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

// prepareInstanceTmp grants only an instance's durable tmp leaf. Its ancestors
// remain supervisor-owned, outside the instance and Team writable trees. The
// caller must already hold the existing writer lease; this adds no second lease.
func prepareInstanceTmp(cfg gateway.Config, spec gateway.GatewayStartSpec) (string, error) {
	switch cfg.RuntimeType {
	case "openclaw", "hermes", "opencode", "deepseek-harness":
	default:
		return "", fmt.Errorf("invalid persistent tmp runtime")
	}
	if spec.UserID < 1 || spec.InstanceID < 1 || spec.UID <= 0 || spec.GID <= 0 || uint64(spec.UID) >= 1<<32-1 || uint64(spec.GID) >= 1<<32-1 || (spec.RuntimeType != "" && spec.RuntimeType != cfg.RuntimeType) {
		return "", fmt.Errorf("invalid persistent tmp identity")
	}
	if err := trustedDirectory(cfg.WorkspaceRoot); err != nil {
		return "", err
	}
	current, err := workspaceio.Open(cfg.WorkspaceRoot)
	if err != nil {
		return "", err
	}
	defer func() { current.Close() }()
	parts := []string{".clawmanager-instance-tmp", cfg.RuntimeType, fmt.Sprintf("user-%d", spec.UserID), fmt.Sprintf("instance-%d", spec.InstanceID), "tmp"}
	for i, part := range parts {
		leaf := i == len(parts)-1
		mode := os.FileMode(0711)
		if leaf {
			mode = 0700
		}
		err := current.Mkdir(part, mode)
		created := err == nil
		if err != nil && !os.IsExist(err) {
			return "", err
		}
		child, err := current.Child(part, mode)
		if err != nil {
			return "", err
		}
		current.Close()
		current = child
		file, err := current.Open(".")
		if err != nil {
			return "", err
		}
		err = prepareTmpDirectory(file, created, leaf, spec.UID, spec.GID)
		file.Close()
		if err != nil {
			return "", err
		}
	}
	// Recheck the named source before handing it to the unprivileged launcher.
	// Trusted, non-writable ancestors prevent business code swapping this leaf.
	return checkedSandboxSource(filepath.Clean(current.Base))
}
