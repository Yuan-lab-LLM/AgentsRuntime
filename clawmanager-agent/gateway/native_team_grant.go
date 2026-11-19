package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

// NativeTeamGrant is management-verified inode identity, never a model field or
// an environment flag. GID numbers inside the child user namespace may differ.
type NativeTeamGrant struct {
	Path          string
	GID           int
	Device, Inode uint64
}

func PinNativeTeamGrant(cfg Config, req CreateGatewayRequest) (*NativeTeamGrant, error) {
	_, shared, enabled := LiteTeamEnvironment(req, req.WorkspacePath)
	if !enabled || cfg.SandboxProfile != "nfs-rootless-v1" {
		return nil, nil
	}
	root, err := openLiteTeamRoot(cfg.WorkspaceRoot, req, req.WorkspacePath, shared)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	gid, err := teamDirectoryGroup(root)
	if err != nil {
		return nil, err
	}
	if gid != liteTeamSharedGID(req) {
		return nil, fmt.Errorf("existing Team root group does not match authorization")
	}
	device, inode, err := nativeTeamDirectoryIdentity(root)
	if err != nil {
		return nil, err
	}
	return &NativeTeamGrant{Path: shared, GID: gid, Device: device, Inode: inode}, nil
}

func nativeTeamGrantForRoot(root *workspaceio.FS, path string, gid int, grant *NativeTeamGrant) bool {
	if grant == nil || grant.Path != path || grant.GID != gid || grant.Inode == 0 {
		return false
	}
	device, inode, err := nativeTeamDirectoryIdentity(root)
	return err == nil && device == grant.Device && inode == grant.Inode
}
