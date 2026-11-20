package slots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func prepareWorkerStorage(cfg gateway.Config, children ...string) error {
	if cfg.SandboxProfile == "nfs-rootless-v1" {
		return workspaceio.PrepareNFSManagementRoots(cfg.WorkspaceRoot, children...)
	}
	return workspaceio.PrepareManagementRoots(cfg.WorkspaceRoot, children...)
}

// PrepareInstanceStorage executes only the fixed grant operation, with the
// instance group selected before mkdir. No NFS chown privilege is required.
func PrepareInstanceStorage(cfg gateway.Config, req gateway.CreateGatewayRequest) error {
	if cfg.SandboxProfile != "nfs-rootless-v1" {
		return nil
	}
	if _, err := gateway.ValidateWorkspacePath(cfg.WorkspaceRoot, cfg.RuntimeType, req); err != nil {
		return err
	}
	if err := runStorageGrant(gateway.StorageGrantRequest{Root: cfg.WorkspaceRoot, RuntimeType: cfg.RuntimeType, UserID: req.UserID, InstanceID: req.InstanceID, UID: req.UID, GID: req.GID}, req.GID); err != nil {
		return err
	}
	tmp := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-instance-tmp", cfg.RuntimeType, fmt.Sprintf("user-%d", req.UserID), fmt.Sprintf("instance-%d", req.InstanceID), "tmp")
	if err := runPrivateStorageAudit(req.UID, req.GID, []string{req.WorkspacePath, tmp}, true); err != nil {
		return err
	}

	_, shared, enabled := gateway.LiteTeamEnvironment(req, req.WorkspacePath)
	if enabled && !strings.HasPrefix(shared, req.WorkspacePath+string(os.PathSeparator)) {
		team, _ := gateway.RequestEnvValue(req, "CLAWMANAGER_TEAM_ID")
		raw, _ := gateway.RequestEnvValue(req, "CLAWMANAGER_TEAM_SHARED_GID")
		gid, err := strconv.Atoi(raw)
		if err != nil || gid <= 0 {
			return fmt.Errorf("invalid Team grant")
		}
		if err = runStorageGrant(gateway.StorageGrantRequest{Root: cfg.WorkspaceRoot, UserID: req.UserID, Team: team, GID: gid}, gid); err != nil {
			return err
		}
	}
	return nil
}

func runStorageGrant(req gateway.StorageGrantRequest, gid int) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "managed-storage-grant")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(payload)
	if err = configureSandboxProcess(cmd, 10001, gid, nil); err != nil {
		return err
	}
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("NFS instance storage grant failed; verify management UID 10001 provisioning: %w", err)
	}
	return nil
}

// Runs after the durable worker admission/writer gates and before any bind.
// Management pins O_PATH grant FDs; the instance helper can traverse the exact
// grants even when management ancestors are0700. No ancestor FD is inherited.
func runPrivateStorageAudit(uid, gid int, paths []string, tmpLast bool) error {
	_, err := auditPrivateStorage(uid, gid, paths, tmpLast, false)
	return err
}

func runPrivateStorageAuditProof(uid, gid int, paths []string, tmpLast bool) ([]gateway.NativeUpgradeRootProof, error) {
	return auditPrivateStorage(uid, gid, paths, tmpLast, true)
}

func auditPrivateStorage(uid, gid int, paths []string, tmpLast, collectProof bool) ([]gateway.NativeUpgradeRootProof, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	request := gateway.PrivateStorageAuditRequest{UID: uid, GID: gid}
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for i, path := range paths {
		if _, err = checkedSandboxSource(path); err != nil {
			return nil, err
		}
		fd, err := syscall.Open(path, 0x200000|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, os.NewFile(uintptr(fd), "private-grant"))
		request.TmpRoots = append(request.TmpRoots, tmpLast && i == len(paths)-1)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/usr/bin/setpriv", "--inh-caps=-all", "--ambient-caps=-all", "--", executable, "managed-private-tree-audit")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(payload)
	cmd.ExtraFiles = files
	if err = configureSandboxProcess(cmd, uid, gid, nil); err != nil {
		return nil, err
	}
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("NFS private inode validation failed: %w", err)
	}
	if !collectProof {
		return nil, nil
	}
	var proof []gateway.NativeUpgradeRootProof
	for i, file := range files {
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("private audit grant identity unavailable")
		}
		proof = append(proof, gateway.NativeUpgradeRootProof{Path: paths[i], Device: uint64(st.Dev), Inode: st.Ino, OwnerUID: st.Uid, OwnerGID: st.Gid, Mode: uint32(st.Mode)})
	}
	return proof, nil
}
