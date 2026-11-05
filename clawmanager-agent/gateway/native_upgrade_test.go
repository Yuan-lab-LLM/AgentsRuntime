package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeUpgradeRejectsUnknownActionAndRemovesLeaseFromChild(t *testing.T) {
	op := NativeUpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", Action: "migrate", Request: WorkspaceUpgradeRequest{RolloutID: "rollout-1", InstanceID: 2, UserID: 1, Generation: 3, UID: 200002, GID: 200002, LeaseToken: "management-token-must-not-cross"}}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	if child := op.Child(); child.Request.LeaseToken != "" {
		t.Fatal("management lease token serialized to child")
	}
	op.Action = "run-any-command"
	if op.Validate() == nil {
		t.Fatal("arbitrary native action accepted")
	}
}

func TestNativeStorageConfigCannotSerializeManagementSecrets(t *testing.T) {
	cfg := Config{ControlToken: "secret-control", ReportToken: "secret-report", GatewayToken: "secret-gateway", LLMAPIKey: "secret-model", LLMBaseURL: "https://secret-upstream.invalid"}
	raw, err := json.Marshal(NativeConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("management configuration entered native payload")
	}
}

func TestScopedNativeUpgradeAllowsOnlyExplicitResumeRead(t *testing.T) {
	op := NativeUpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", Action: "migrate", Request: WorkspaceUpgradeRequest{RolloutID: "current", ResumeRolloutID: "previous", ResumeGeneration: 2, RecoveryMode: "retry", InstanceID: 2, UserID: 1, Generation: 3, UID: 200002, GID: 200002}}
	host := &scopedUpgradeHost{op: op, cfg: Config{WorkspaceRoot: "/workspaces", RuntimeType: "openclaw"}}
	prior := op.Request
	prior.RolloutID = "previous"
	prior.Generation = 2
	if path, err := host.WorkspaceForUpgrade(prior); err != nil || path != "/workspaces/openclaw/user-1/instance-2" {
		t.Fatalf("authorized resume read rejected: %s %v", path, err)
	}
	if err := host.VerifyUpgradeWriter(context.Background(), prior); err == nil {
		t.Fatal("prior capsule identity authorized a current writer")
	}
	prior.RolloutID = "other"
	if _, err := host.WorkspaceForUpgrade(prior); err == nil {
		t.Fatal("unrequested prior capsule accepted")
	}
}

func TestNativeUpgradeRejectsCredentialIntegerWrap(t *testing.T) {
	op := NativeUpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", Action: "inventory", Request: WorkspaceUpgradeRequest{RolloutID: "r", UserID: 1, InstanceID: 2, Generation: 1, UID: 1 << 32, GID: 1 << 32}}
	if op.Validate() == nil {
		t.Fatal("UID that wraps to root was accepted")
	}
}
