package scheduledtasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReconcileOpenClaw81UsesDeclarationsAndPreservesUserAutomations(t *testing.T) {
	payload := map[string]any{"schemaVersion": 1, "items": []any{
		map[string]any{"id": 9, "type": "scheduled_task", "key": "daily", "name": "Daily", "content": map[string]any{
			"schemaVersion": 1, "kind": "scheduled_task", "format": "task/openclaw-cron@v1", "config": map[string]any{
				"name": "Daily brief", "enabled": true,
				"schedule":      map[string]any{"kind": "cron", "expr": "0 9 * * *", "tz": "Asia/Shanghai"},
				"sessionTarget": "isolated", "wakeMode": "now",
				"payload": map[string]any{"kind": "agentTurn", "message": "brief"},
			},
		}},
	}}
	raw, _ := json.Marshal(payload)
	runner := &automationRunnerStub{listOutput: []byte(`{"jobs":[{"id":"managed-old","declarationKey":"clawmanager:scheduled-task:8"},{"id":"user-job","declarationKey":"user:job"},{"id":"managed-current","declarationKey":"clawmanager:scheduled-task:9"}]}`)}
	result, err := ReconcileOpenClaw81(context.Background(), string(raw), 31000, GatewayAuth{Mode: "token", Token: "secret-token"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Removed != 1 {
		t.Fatalf("result = %+v", result)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, required := range []string{"automations add", "--declaration-key clawmanager:scheduled-task:9", "--cron 0 9 * * *", "--tz Asia/Shanghai", "--message brief", "automations remove managed-old"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("commands missing %q:\n%s", required, joined)
		}
	}
	if strings.Contains(joined, "remove user-job") || strings.Contains(joined, "remove managed-current") {
		t.Fatalf("unexpected removal:\n%s", joined)
	}
	for _, env := range runner.envs {
		if envValueForTest(env, "OPENCLAW_GATEWAY_TOKEN") != "secret-token" || envValueForTest(env, "OPENCLAW_GATEWAY_PASSWORD") != "" {
			t.Fatalf("env = %#v", env)
		}
	}
}

func TestReconcileOpenClaw81UsesTrustedProxyPasswordAndClearsAmbientToken(t *testing.T) {
	runner := &automationRunnerStub{listOutput: []byte(`{"jobs":[]}`)}
	_, err := ReconcileOpenClaw81(context.Background(), `{"schemaVersion":1,"items":[]}`, 31000, GatewayAuth{
		Mode: "trusted-proxy", Password: "instance-secret", Token: "ambient-token",
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range runner.envs {
		if envValueForTest(env, "OPENCLAW_GATEWAY_TOKEN") != "" {
			t.Fatalf("trusted-proxy env retained token: %#v", env)
		}
		if envValueForTest(env, "OPENCLAW_GATEWAY_PASSWORD") != "instance-secret" {
			t.Fatalf("trusted-proxy env missing instance password: %#v", env)
		}
	}
	merged := mergeCommandEnv([]string{"PATH=/bin", "OPENCLAW_GATEWAY_TOKEN=stale"}, runner.envs[0])
	if envValueForTest(merged, "OPENCLAW_GATEWAY_TOKEN") != "" {
		t.Fatalf("ambient gateway token survived replacement: %#v", merged)
	}
}

func envValueForTest(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

type automationRunnerStub struct {
	commands   []string
	envs       [][]string
	listOutput []byte
}

func (r *automationRunnerStub) Run(_ context.Context, args, env []string) ([]byte, error) {
	r.commands = append(r.commands, strings.Join(args, " "))
	r.envs = append(r.envs, append([]string(nil), env...))
	if len(args) >= 2 && args[0] == "automations" && args[1] == "list" {
		return r.listOutput, nil
	}
	return []byte(`{"ok":true}`), nil
}
