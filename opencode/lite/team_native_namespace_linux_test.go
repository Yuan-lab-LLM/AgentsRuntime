//go:build linux

package opencode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestOpenCodeTeamNativeNamespaceChild(t *testing.T) {
	raw := os.Getenv("CM_OPENCODE_NATIVE_FIXTURE")
	if raw == "" {
		return
	}
	var op gateway.NativeStorageRequest
	if err := json.Unmarshal([]byte(raw), &op); err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{RuntimeType: "opencode", WorkspaceRoot: op.Config.WorkspaceRoot, SandboxProfile: "nfs-rootless-v1", NativeTeamGrant: op.Config.TeamGrant}
	profile := NewProfile("opencode")
	for i := 0; i < 2; i++ {
		if err := profile.PrepareWorkspace(cfg, op.Request, op.Request.WorkspacePath); err != nil {
			t.Fatal(err)
		}
	}
	link, err := os.Readlink(filepath.Join(op.Request.WorkspacePath, "starter", "team"))
	if err != nil || link != op.Request.Environment["CLAWMANAGER_TEAM_SHARED_DIR"] {
		t.Fatalf("project Team alias: %q, %v", link, err)
	}
	values := profile.GatewayEnv(nil, cfg, op.Request, op.Request.WorkspacePath, 20000)
	for _, entry := range values {
		if entry == "CLAWMANAGER_TEAM_SHARED_GID="+op.Request.Environment["CLAWMANAGER_TEAM_SHARED_GID"] {
			return
		}
	}
	t.Fatal("authorized Team group was not forwarded")
}

// Runs the complete OpenCode workspace preparation under the actual nested user
// namespace. A root-only helper test misses attempts to chgrp shared NFS data.
func TestOpenCodeTeamNativeNamespace(t *testing.T) {
	if os.Getenv("CM_OPENCODE_REAL_NAMESPACE") != "1" {
		t.Skip("explicit rootless namespace fixture")
	}
	uid, gid := os.Geteuid(), os.Getegid()
	sharedGID, err := strconv.Atoi(os.Getenv("CM_OPENCODE_SHARED_GID"))
	if err != nil || sharedGID == gid || uid < 200001 {
		t.Fatal("requires distinct authorized supplemental group")
	}
	base := t.TempDir()
	workspace := filepath.Join(base, "opencode", "user-1", "instance-"+strconv.Itoa(uid-200000))
	shared := filepath.Join(base, "teams", "user-1", "team-7-shared")
	for _, p := range []string{filepath.Join(workspace, "home"), filepath.Join(workspace, "starter"), shared} {
		if err := os.MkdirAll(p, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(shared, uid, sharedGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	// A pre-existing artifact must retain its bytes, mode and ownership.
	artifact := filepath.Join(shared, "existing.md")
	if err := os.WriteFile(artifact, []byte("existing Team result\n"), 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{UserID: 1, InstanceID: uid - 200000, UID: uid, GID: gid, WorkspacePath: workspace, ProjectRelativePath: "starter", Environment: map[string]string{
		"CLAWMANAGER_TEAM_ENABLED": "true", "CLAWMANAGER_TEAM_ID": "7", "CLAWMANAGER_TEAM_SHARED_DIR": shared,
		"CLAWMANAGER_TEAM_SHARED_GID": strconv.Itoa(sharedGID), "CLAWMANAGER_TEAM_CONFIG_JSON": `{"fixture":true}`,
	}}
	cfg := gateway.Config{WorkspaceRoot: base, SandboxProfile: "nfs-rootless-v1"}
	grant, err := gateway.PinNativeTeamGrant(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []bool{false, true} {
		op := gateway.NativeStorageRequest{Config: gateway.NativeStorageConfig{WorkspaceRoot: base}, Request: req}
		if proof {
			op.Config.TeamGrant = grant
		}
		payload, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bwrap", "--unshare-user", "--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid), "--ro-bind", "/", "/", "--bind", base, base, "--", exe, "-test.run=^TestOpenCodeTeamNativeNamespaceChild$", "-test.v")
		cmd.Env = append(os.Environ(), "CM_OPENCODE_NATIVE_FIXTURE="+string(payload))
		out, runErr := cmd.CombinedOutput()
		if proof && runErr != nil {
			t.Fatalf("complete protected preparation: %v %s", runErr, out)
		}
		if !proof && runErr == nil {
			t.Fatal("accepted unmapped Team group without proof")
		}
	}
	after, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || string(data) != "existing Team result\n" {
		t.Fatal("existing artifact changed")
	}
}
