package openclaw

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestUpgradeCommandMountsOnlyOwnInstanceAndPrivateCapsule(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "openclaw", "user-2", "instance-7")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", Runtime: Profile{}, IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}}
	host := gateway.NewGatewayManager(cfg, nil, nil)
	m := &upgradeService{cfg: cfg, host: host}
	uid, gid := assignedUpgradeTestIdentity()
	req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 1, UID: uid, GID: gid, RolloutID: "12"}
	closeIO, err := m.beginIO(req, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIO()
	probe := filepath.Join(m.io.controlRoot(), "probe")
	if err = m.io.MkdirAll(probe, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = probe
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + probe}
	spec, err := m.upgradeSandboxSpec(cmd, req, true)
	if err != nil {
		t.Fatal(err)
	}
	if spec.UID != req.UID || spec.GID != req.GID || spec.UID <= 0 || spec.GID <= 0 {
		t.Fatalf("sandbox lost assigned non-root identity: uid=%d gid=%d", spec.UID, spec.GID)
	}
	for _, invalid := range []WorkspaceUpgradeRequest{
		{UID: 0, GID: req.GID}, {UID: req.UID, GID: 0},
	} {
		invalid.InstanceID, invalid.UserID, invalid.Generation, invalid.RolloutID = req.InstanceID, req.UserID, req.Generation, req.RolloutID
		if _, err := m.upgradeSandboxSpec(cmd, invalid, true); err == nil {
			t.Fatal("root business identity accepted")
		}
	}
	if len(spec.Mounts) != 2 || spec.Mounts[0].Source != workspace || !spec.Mounts[0].ReadOnly {
		t.Fatalf("preflight source must be own readonly instance: %+v", spec.Mounts)
	}
	if spec.Mounts[1].Source != probe || spec.Mounts[1].ReadOnly {
		t.Fatalf("probe requires only narrow writable capsule: %+v", spec.Mounts)
	}
	cmd.Dir = filepath.Join(root, "openclaw", "user-2", "instance-8")
	if _, err = m.upgradeSandboxSpec(cmd, req, true); err == nil {
		t.Fatal("sibling instance authorized for upgrade command")
	}
	cmd.Dir = probe
	cmd.ExtraFiles = []*os.File{os.Stdin}
	if _, err = m.upgradeSandboxSpec(cmd, req, false); err == nil {
		t.Fatal("management descriptor inherited by plugin command")
	}
}

func TestUpgradeCommandLegacyRunnerAndBoundedOutput(t *testing.T) {
	m := &upgradeService{}
	output, err := m.combinedUpgradeOutput(context.Background(), exec.Command("/bin/sh", "-c", "printf legacy"), WorkspaceUpgradeRequest{})
	if err != nil || string(output) != "legacy" {
		t.Fatalf("legacy runner changed: %q %v", output, err)
	}
	var bounded limitedUpgradeOutput
	data := make([]byte, 128<<10)
	if n, err := bounded.Write(data); err != nil || n != len(data) || bounded.Len() != 64<<10 {
		t.Fatal("unbounded plugin command output")
	}
}

func TestUpgradeCommandRejectsSiblingPathsInsideGeneralMountRoots(t *testing.T) {
	for _, root := range []string{"/tmp/cm-upgrade-scope-test", "/opt/cm-upgrade-scope-test"} {
		t.Run(root, func(t *testing.T) {
			cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", IsolatedLifecycle: true, GatewayBaseEnv: []string{"PATH=/usr/bin:/bin"}}
			req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 1, UID: 200007, GID: 200007, RolloutID: "scope"}
			workspace := filepath.Join(root, "openclaw", "user-2", "instance-7")
			sibling := filepath.Join(root, "openclaw", "user-2", "instance-8")
			m := &upgradeService{cfg: cfg, host: gateway.NewGatewayManager(cfg, nil, nil), io: &upgradeIO{pool: root}}
			command := func() *exec.Cmd {
				cmd := exec.Command("/bin/sh", "-c", "true")
				cmd.Dir = workspace
				cmd.Env = []string{"HOME=" + filepath.Join(workspace, "home")}
				return cmd
			}
			if spec, err := m.upgradeSandboxSpec(command(), req, true); err != nil || len(spec.Mounts) != 1 || spec.Mounts[0].Source != workspace {
				t.Fatalf("own instance rejected: %+v %v", spec.Mounts, err)
			}
			for _, tc := range []struct {
				name   string
				modify func(*exec.Cmd)
			}{
				{"directory", func(cmd *exec.Cmd) { cmd.Dir = sibling }},
				{"absolute_argument", func(cmd *exec.Cmd) { cmd.Args = append(cmd.Args, sibling) }},
				{"home", func(cmd *exec.Cmd) { cmd.Env = []string{"HOME=" + sibling} }},
				{"config", func(cmd *exec.Cmd) {
					cmd.Env = []string{"OPENCLAW_CONFIG_PATH=" + filepath.Join(sibling, "openclaw.json")}
				}},
				{"temporary_files", func(cmd *exec.Cmd) { cmd.Env = []string{"TMPDIR=" + sibling} }},
				{"management_parent", func(cmd *exec.Cmd) { cmd.Args = append(cmd.Args, filepath.Join(root, ".clawmanager-upgrades")) }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cmd := command()
					tc.modify(cmd)
					if _, err := m.upgradeSandboxSpec(cmd, req, true); err == nil {
						t.Fatal("another workspace or management path authorized through general mount root")
					}
				})
			}
		})
	}
}

func TestProtectedStoppedMaintenanceNeverUsesOuterNetwork(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		marker := filepath.Join(t.TempDir(), "executed")
		m := &upgradeService{cfg: gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}}
		ctx := context.Background()
		if readOnly {
			ctx = withReadOnlyUpgradeCommands(ctx)
		}
		err := m.runUpgradeCommand(ctx, exec.Command("/bin/sh", "-c", `touch "$1"`, "probe", marker), WorkspaceUpgradeRequest{})
		if err == nil {
			t.Fatal("protected maintenance entered outer network")
		}
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("protected command executed before separate authorization")
		}
	}
}
