package gateway

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func expectedPodRequest(t *testing.T, pod string) WorkspaceUpgradeRequest {
	t.Helper()
	var req WorkspaceUpgradeRequest
	raw := `{"rollout_id":"r1","user_id":1,"instance_id":2,"generation":3,"uid":200002,"gid":200002,"lease_token":"lease","expected_pod_uid":"` + pod + `"}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestUpgradeExpectedPodRejectsBeforeLeaseWrites(t *testing.T) {
	root := t.TempDir()
	m := NewGatewayManager(Config{WorkspaceRoot: root, RuntimeType: "opencode", PodUID: "new"}, nil, nil)
	if _, err := m.AcquireWriterLease(expectedPodRequest(t, "old"), "lease", time.Minute); err == nil {
		t.Fatal("stale Pod UID acquired a lease")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("stale Pod UID mutated storage")
	}
	req := expectedPodRequest(t, "new")
	if _, err := m.AcquireWriterLease(req, "lease", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseWriterLease(req); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeExpectedPodScopesNativeChildAndConfig(t *testing.T) {
	cfg := Config{WorkspaceRoot: "/workspaces", RuntimeType: "opencode", PodUID: "new"}
	if got := NativeConfig(cfg).config(nil); got.PodUID != "new" {
		t.Fatal("native child lost actual Pod UID")
	}
	for _, pod := range []string{"old", "new", ""} {
		req := expectedPodRequest(t, pod)
		req.LeaseToken = ""
		host := scopedUpgradeHost{cfg: cfg, op: NativeUpgradeOperation{Request: req}}
		_, err := host.WorkspaceForUpgrade(req)
		if (pod == "old") != (err != nil) {
			t.Fatalf("child Pod UID scope %q: %v", pod, err)
		}
	}
}

func TestUpgradeExpectedPodAndCompatibilityCorrelationAreOptional(t *testing.T) {
	req := expectedPodRequest(t, "")
	raw, _ := json.Marshal(req)
	if string(raw) == "" {
		t.Fatal("empty request")
	}
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	if _, ok := fields["expected_pod_uid"]; ok {
		t.Fatal("legacy request gained a field")
	}
	var result UpgradeCompatibilityResult
	json.Unmarshal([]byte(`{"rollout_id":"r1","generation":3}`), &result)
	raw, _ = json.Marshal(result)
	fields = nil
	json.Unmarshal(raw, &fields)
	if fields["rollout_id"] != "r1" || fields["generation"] != float64(3) {
		t.Fatal("compatibility dropped correlation IDs")
	}
}
