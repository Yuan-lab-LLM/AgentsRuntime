package gateway

import (
	"context"
	"strings"
	"testing"
)

func TestOpenClawCapabilityFlagDependencies(t *testing.T) {
	for _, p := range []string{"", "0", "1"} {
		for _, r := range []string{"", "0", "1"} {
			for _, e := range []string{"", "0", "1"} {
				env := []string{taskProtocolFlag + "=" + p, renewalFlag + "=" + r, enrollmentFlag + "=" + e}
				_, err := capabilityFlags(env)
				wantInvalid := r == "1" && p != "1" || e == "1" && r != "1"
				if (err != nil) != wantInvalid {
					t.Fatalf("combination %q: %v", env, err)
				}
			}
		}
	}
	for _, flag := range []string{taskProtocolFlag, renewalFlag, enrollmentFlag} {
		for _, env := range [][]string{{flag + "=yes"}, {flag + "=0", flag + "=0"}, {flag + "=0", flag + "=1"}} {
			if checkTaskActivation(context.Background(), env, func(context.Context, []string) ([]byte, error) {
				t.Fatal("invalid flags invoked probe")
				return nil, nil
			}) == nil {
				t.Fatal("invalid flags passed")
			}
		}
	}
}
func TestOpenClawFinalCapabilityEnvironment(t *testing.T) {
	accepted := []byte(`{"runtime_version":"2026.10.2","adapter_version":"cm-openclaw-native-1","artifact_sha256":"` + strings.Repeat("a", 64) + `","integrity_verified":true,"node_version_matches":true,"release_accepted":true,"activation_ready":true}`)
	base := []string{taskProtocolFlag + "=1", renewalFlag + "=1", enrollmentFlag + "=1", "CLAWMANAGER_LLM_BASE_URL=http://gateway.invalid/api/gateway/llm", "CLAWMANAGER_LLM_API_KEY=test-key", "CLAWMANAGER_GATEWAY_GENERATION=7", "CLAWMANAGER_TASK_RECEIPT_DIR=/trusted/instance-1"}
	calls := 0
	probe := func(context.Context, []string) ([]byte, error) { calls++; return accepted, nil }
	if err := checkRuntimeActivation(context.Background(), base, probe); err != nil || calls != 1 {
		t.Fatalf("valid configuration: %v calls=%d", err, calls)
	}
	for _, entry := range []string{
		"CLAWMANAGER_LLM_BASE_URL=file:///tmp/key", "CLAWMANAGER_LLM_BASE_URL=https://key:secret@host", "CLAWMANAGER_LLM_BASE_URL=https://host/?key=secret",
		"CLAWMANAGER_LLM_API_KEY=", "CLAWMANAGER_LLM_API_KEY=key\nsecret", "CLAWMANAGER_GATEWAY_GENERATION=", "CLAWMANAGER_GATEWAY_GENERATION=-1", "CLAWMANAGER_GATEWAY_GENERATION=07", "CLAWMANAGER_GATEWAY_GENERATION=2147483648",
		"CLAWMANAGER_TASK_RECEIPT_DIR=", "CLAWMANAGER_TASK_RECEIPT_DIR=/", "CLAWMANAGER_TASK_RECEIPT_DIR=relative", "CLAWMANAGER_TASK_RECEIPT_DIR=/trusted/../other",
	} {
		key, _, _ := strings.Cut(entry, "=")
		env := append([]string(nil), base...)
		for i, v := range env {
			if strings.HasPrefix(v, key+"=") {
				env[i] = entry
			}
		}
		calls = 0
		if checkRuntimeActivation(context.Background(), env, probe) == nil || calls != 0 {
			t.Fatalf("invalid final config accepted: %s", key)
		}
	}
	for _, entry := range base[3:] {
		calls = 0
		if checkRuntimeActivation(context.Background(), append(append([]string(nil), base...), entry), probe) == nil || calls != 0 {
			t.Fatal("duplicate identity accepted")
		}
	}
	calls = 0
	if checkRuntimeActivation(context.Background(), []string{"CLAWMANAGER_LLM_API_KEY=", "CLAWMANAGER_GATEWAY_GENERATION=bad"}, probe) != nil || calls != 0 {
		t.Fatal("legacy mode changed")
	}
	if checkRuntimeActivation(context.Background(), base, func(context.Context, []string) ([]byte, error) {
		return []byte(strings.Replace(string(accepted), `"release_accepted":true`, `"release_accepted":false`, 1)), nil
	}) == nil {
		t.Fatal("release gate bypassed")
	}
}
