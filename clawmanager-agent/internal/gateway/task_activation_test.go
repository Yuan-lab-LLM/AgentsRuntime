package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenClawTaskActivationGate(t *testing.T) {
	valid := `{"runtime_version":"2026.10.2","adapter_version":"cm-openclaw-native-1","artifact_sha256":"` + strings.Repeat("a", 64) + `","integrity_verified":true,"node_version_matches":true,"release_accepted":true,"activation_ready":true}`
	for _, value := range []string{"", "0"} {
		if err := checkTaskActivation(context.Background(), []string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=" + value}, func(context.Context, []string) ([]byte, error) { t.Fatal("legacy ran verifier"); return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", "invalid", strings.Repeat("x", 4097), strings.Replace(valid, `"activation_ready":true`, `"activation_ready":false`, 1), strings.Replace(valid, "2026.10.2", "2026.8.1", 1), strings.Replace(valid, `"release_accepted":true`, `"release_accepted":false`, 1), strings.Replace(valid, `"node_version_matches":true`, `"node_version_matches":false`, 1)} {
		err := checkTaskActivation(context.Background(), []string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=1"}, func(context.Context, []string) ([]byte, error) { return []byte(raw), nil })
		if err == nil {
			t.Fatal("unverified activation accepted")
		}
	}
	env := []string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=1", "NODE_OPTIONS=--import private", "NODE_PATH=/user", "OPENCLAW_PACKAGE_ROOT=/user", "KEEP=value"}
	err := checkTaskActivation(context.Background(), env, func(ctx context.Context, got []string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		for _, v := range got {
			if strings.HasPrefix(v, "NODE_") || strings.HasPrefix(v, "OPENCLAW_PACKAGE_ROOT=") {
				t.Fatal("injected verifier environment")
			}
		}
		return []byte(valid), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 5 {
		t.Fatal("input mutated")
	}
	for _, env := range [][]string{{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=yes"}, {"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=0", "CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=1"}} {
		if checkTaskActivation(context.Background(), env, func(context.Context, []string) ([]byte, error) {
			t.Fatal("invalid setting ran verifier")
			return nil, nil
		}) == nil {
			t.Fatal("invalid flag accepted")
		}
	}
	err = checkTaskActivation(context.Background(), []string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=1"}, func(context.Context, []string) ([]byte, error) { return nil, errors.New("private credential") })
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("probe failure leaked detail or passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checkTaskActivation(ctx, []string{"CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=1"}, func(context.Context, []string) ([]byte, error) { return []byte(valid), nil }) == nil {
		t.Fatal("cancelled probe passed")
	}
}

func TestOpenClawTaskActivationOutputBound(t *testing.T) {
	output := &activationProbeOutput{}
	if n, err := output.Write([]byte(strings.Repeat("x", 4096))); n != 4096 || err != nil {
		t.Fatal("valid output rejected")
	}
	if n, err := output.Write([]byte("private-overflow")); n != 0 || err == nil {
		t.Fatal("overflow accepted")
	}
	if len(output.data) != 4096 {
		t.Fatal("output buffer exceeded bound")
	}
}
