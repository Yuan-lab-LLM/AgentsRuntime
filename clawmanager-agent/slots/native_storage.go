package slots

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
)

func (s sandboxStarter) PrepareNativeStorage(ctx context.Context, req gateway.CreateGatewayRequest, port int) error {
	if s.cfg.SandboxProfile != "nfs-rootless-v1" {
		return fmt.Errorf("credential-scoped storage profile required")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(gateway.NativeStorageRequest{Config: gateway.NativeConfig(s.cfg), Request: req, Port: port})
	if err != nil {
		return err
	}
	spec := gateway.GatewayStartSpec{GatewayID: fmt.Sprintf("gw-%d-%d", req.InstanceID, req.Generation), RuntimeType: s.cfg.RuntimeType, InstanceID: req.InstanceID, UserID: req.UserID, WorkspacePath: req.WorkspacePath, Port: port, UID: req.UID, GID: req.GID, Generation: req.Generation, Command: []string{executable, "managed-native-storage"}, Stdin: payload, Env: s.cfg.Runtime.GatewayEnv(s.cfg.GatewayBaseEnv, s.cfg, req, req.WorkspacePath, port)}
	_, err = s.RunGatewayCommand(ctx, spec)
	return err
}
