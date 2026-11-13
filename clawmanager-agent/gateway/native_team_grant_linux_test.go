//go:build linux

package gateway

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestNativeTeamGrantNamespaceChild(t *testing.T) {
	raw := os.Getenv("CM_TEAM_GRANT_FIXTURE")
	if raw == "" {
		return
	}
	var op NativeStorageRequest
	if err := json.Unmarshal([]byte(raw), &op); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkspaceRoot: op.Config.WorkspaceRoot, SandboxProfile: "nfs-rootless-v1", NativeTeamGrant: op.Config.TeamGrant}
	if os.Getenv("CM_TEAM_GRANT_CONFIG_ONLY") == "1" {
		if err := WriteLiteTeamConfigJSONForConfig(cfg, op.Request, op.Request.WorkspacePath); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := PrepareLiteTeamSharedWorkspaceForConfig(cfg, op.Request, op.Request.WorkspacePath, filepath.Join(op.Request.WorkspacePath, "home")); err != nil {
		t.Fatal(err)
	}
	if err := WriteLiteTeamConfigJSONForConfig(cfg, op.Request, op.Request.WorkspacePath); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTeamGrantRealUserNamespace(t *testing.T) {
	if os.Getenv("CM_TEAM_GRANT_REAL_NAMESPACE") != "1" {
		t.Skip("explicit rootless namespace fixture")
	}
	uid, gid := os.Geteuid(), os.Getegid()
	sharedGID, err := strconv.Atoi(os.Getenv("CM_TEAM_GRANT_SHARED_GID"))
	if err != nil || sharedGID == gid || uid < 200001 {
		t.Fatal("requires mapped instance and distinct authorized supplemental group")
	}
	base := t.TempDir()
	workspace := filepath.Join(base, "hermes", "user-1", "instance-"+strconv.Itoa(uid-200000))
	shared := filepath.Join(base, "teams", "user-1", "team-7-shared")
	for _, path := range []string{filepath.Join(workspace, "home"), shared} {
		if err = os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Chown(shared, uid, sharedGID); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(shared, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	req := CreateGatewayRequest{UserID: 1, InstanceID: uid - 200000, UID: uid, GID: gid, WorkspacePath: workspace, Environment: map[string]string{"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_ID": "7", "CLAWMANAGER_TEAM_SHARED_GID": strconv.Itoa(sharedGID), "CLAWMANAGER_TEAM_SHARED_DIR": shared, "CLAWMANAGER_TEAM_CONFIG_JSON": `{"fixture":true}`}}
	cfg := Config{WorkspaceRoot: base, SandboxProfile: "nfs-rootless-v1"}
	grant, err := PinNativeTeamGrant(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	for _, proof := range []bool{false, true} {
		op := NativeStorageRequest{Config: NativeStorageConfig{WorkspaceRoot: base}, Request: req}
		if proof {
			op.Config.TeamGrant = grant
		}
		payload, _ := json.Marshal(op)
		cmd := exec.Command("bwrap", "--unshare-user", "--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid), "--ro-bind", "/", "/", "--bind", base, base, "--", exe, "-test.run=^TestNativeTeamGrantNamespaceChild$", "-test.v")
		cmd.Env = append(os.Environ(), "CM_TEAM_GRANT_FIXTURE="+string(payload))
		output, runErr := cmd.CombinedOutput()
		if proof && runErr != nil {
			t.Fatalf("pinned namespace grant: %v %s", runErr, output)
		}
		if !proof && runErr == nil {
			t.Fatal("unmapped group accepted without management proof")
		}
	}
}
