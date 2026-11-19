package slots

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
)

func (s sandboxStarter) CheckNativeWorkspaceHealth(ctx context.Context, spec gateway.GatewayStartSpec) error {
	if s.cfg.SandboxProfile != "nfs-rootless-v1" {
		return fmt.Errorf("scoped health requires NFS profile")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(gateway.NativeWorkspaceHealthRequest{Root: s.cfg.WorkspaceRoot, RuntimeType: s.cfg.RuntimeType, Workspace: spec.WorkspacePath, InstanceID: spec.InstanceID, UserID: spec.UserID, Generation: spec.Generation, UID: spec.UID, GID: spec.GID})
	if err != nil {
		return err
	}
	spec.Command = []string{executable, "managed-native-workspace-health"}
	spec.Stdin = payload
	spec.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	_, err = s.RunGatewayCommand(ctx, spec)
	return err
}
