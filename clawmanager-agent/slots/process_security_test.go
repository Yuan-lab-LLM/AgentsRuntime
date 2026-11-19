package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReviewRejectAliasedWorkspaceAndTeamSources(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real, alias := filepath.Join(root, "real"), filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedSandboxSource(alias); err == nil {
		t.Fatal("source alias accepted instead of rejected")
	}
	if got, err := checkedSandboxSource(real); err != nil || got != real {
		t.Fatalf("valid source: %s, %v", got, err)
	}
}

func TestReviewTeamGroupMustMatchAuthorizedDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	shared := filepath.Join(root, "teams", "user-1", "team-7-shared")
	for _, path := range []string{workspace, shared} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gid, err := sandboxDirectoryGroup(shared)
	if err != nil {
		t.Fatal(err)
	}
	if gid == 0 {
		t.Skip("test requires nonzero fixture group")
	}
	cfg := gateway.Config{WorkspaceRoot: root}
	spec := gateway.GatewayStartSpec{WorkspacePath: workspace, UserID: 1, InstanceID: 2, UID: 200001, GID: 200001}
	values := map[string]string{"CLAWMANAGER_TEAM_SHARED_DIR": shared, "CLAWMANAGER_TEAM_ID": "7", "CLAWMANAGER_TEAM_SHARED_GID": strconv.Itoa(int(gid))}
	mounts, groups, err := authorizedSandboxMounts(cfg, spec, values)
	if err != nil || len(mounts) != 2 || len(groups) != 1 || groups[0] != gid {
		t.Fatalf("valid team mount: %v %v %v", mounts, groups, err)
	}
	values["CLAWMANAGER_TEAM_SHARED_GID"] = strconv.Itoa(int(gid) + 1)
	if _, _, err := authorizedSandboxMounts(cfg, spec, values); err == nil {
		t.Fatal("granted supplemental group unrelated to shared directory")
	}
	values["CLAWMANAGER_TEAM_SHARED_GID"] = strconv.Itoa(int(gid))
	values["CLAWMANAGER_TEAM_ID"] = "7/../../escape"
	if _, _, err := authorizedSandboxMounts(cfg, spec, values); err == nil {
		t.Fatal("accepted traversing Team identity")
	}
}
