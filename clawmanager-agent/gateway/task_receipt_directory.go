package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const TaskReceiptRootEnv = "CLAWMANAGER_TASK_RECEIPT_ROOT"

func ReceiptDirectoryPath(root, workspace string, req CreateGatewayRequest) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(workspace) || filepath.Clean(root) == filepath.VolumeName(root)+string(filepath.Separator) || req.InstanceID <= 0 || req.UserID <= 0 || req.UID <= 0 || req.GID <= 0 || req.UID > 2147483647 || req.GID > 2147483647 {
		return "", fmt.Errorf("invalid managed receipt storage configuration")
	}
	root = filepath.Clean(root)
	if workspace != "" {
		workspace = filepath.Clean(workspace)
		for _, pair := range [][2]string{{workspace, root}, {root, workspace}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("receipt storage must be separate from workspaces")
			}
		}
	}
	return filepath.Join(root, "user-"+strconv.Itoa(req.UserID)+"-instance-"+strconv.Itoa(req.InstanceID)), nil
}
func PrepareOpenClawReceiptDirectory(cfg Config, req CreateGatewayRequest) error {
	root := os.Getenv(TaskReceiptRootEnv)
	if root == "" {
		return nil
	}
	target, err := ReceiptDirectoryPath(root, cfg.WorkspaceRoot, req)
	if err != nil {
		return err
	}
	return prepareReceiptDirectory(root, filepath.Base(target), req.UID, req.GID)
}
