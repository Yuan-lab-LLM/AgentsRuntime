package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io"
)

// NativeWorkspaceHealthRequest contains identity/path metadata only. It cannot
// carry management config, credentials, environment, shell or arbitrary paths.
type NativeWorkspaceHealthRequest struct {
	Root, RuntimeType, Workspace             string
	InstanceID, UserID, Generation, UID, GID int
}
type NativeWorkspaceHealthValidator interface {
	CheckNativeWorkspaceHealth(CreateGatewayRequest) error
}

func runNativeWorkspaceHealth(profile RuntimeProfile, in io.Reader) error {
	if err := protectNativeStorageHelper(); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(in, 4097))
	decoder.DisallowUnknownFields()
	var op NativeWorkspaceHealthRequest
	if err := decoder.Decode(&op); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing workspace health request")
	}
	if profile == nil || profile.Type() != op.RuntimeType || op.InstanceID < 1 || op.UserID < 1 || op.Generation < 1 {
		return fmt.Errorf("invalid workspace health identity")
	}
	if err := workspaceio.ValidateInstanceCaller(op.UID, op.GID); err != nil {
		return err
	}
	req := CreateGatewayRequest{InstanceID: op.InstanceID, UserID: op.UserID, Generation: op.Generation, UID: op.UID, GID: op.GID, AgentType: op.RuntimeType, WorkspacePath: op.Workspace}
	if _, err := ValidateWorkspacePath(op.Root, op.RuntimeType, req); err != nil {
		return err
	}
	validator, ok := profile.(NativeWorkspaceHealthValidator)
	if !ok {
		return fmt.Errorf("native workspace health validator unavailable")
	}
	return validator.CheckNativeWorkspaceHealth(req)
}
