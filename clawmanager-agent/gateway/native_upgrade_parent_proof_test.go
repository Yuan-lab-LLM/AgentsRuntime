package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeUpgradeInternalParentProofRoundTrip(t *testing.T) {
	raw := `{"config":{},"operation":{},"parent_proof":{"actor_uid":200007,"actor_gid":200007,"roots":[{"path":"/workspaces/opencode/user-1/instance-7","device":23,"inode":41,"owner_uid":10001,"owner_gid":200007,"mode":16888}]}}`
	var payload NativeUpgradeHelperRequest
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(out, &fields)
	if !strings.Contains(string(fields["parent_proof"]), `"inode":41`) {
		t.Fatalf("trusted pinned grant witness lost: %s", out)
	}
	legacy, _ := json.Marshal(NativeUpgradeHelperRequest{})
	if strings.Contains(string(legacy), "parent_proof") {
		t.Fatal("legacy internal helper gained a proof field")
	}
	// Public operations remain independent of trusted launcher evidence.
	public, _ := json.Marshal(WorkspaceUpgradeRequest{})
	if strings.Contains(string(public), "parent_proof") {
		t.Fatal("parent proof leaked into the public upgrade request")
	}
}

func TestNativeUpgradeParentProofKeepsOtherRuntimeHelperBytes(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes", "deepseek-harness"} {
		cfg := NativeStorageConfig{RuntimeType: kind}
		op := NativeUpgradeOperation{Action: "inventory", Request: WorkspaceUpgradeRequest{RolloutID: "legacy", InstanceID: 7}}
		before, _ := json.Marshal(struct {
			Config    NativeStorageConfig    `json:"config"`
			Operation NativeUpgradeOperation `json:"operation"`
		}{cfg, op})
		after, _ := json.Marshal(NativeUpgradeHelperRequest{Config: cfg, Operation: op})
		if !bytes.Equal(before, after) {
			t.Fatalf("%s legacy helper bytes changed: %s", kind, after)
		}
	}
}
