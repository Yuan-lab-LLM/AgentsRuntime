//go:build unix

package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteOpenClawGatewayConfigChownsScheduledTasksJobsFile(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "openclaw", "user-1", "instance-8")
	uid := os.Getuid()
	gid := os.Getgid()
	payload := map[string]any{
		"schemaVersion": 1,
		"items": []map[string]any{
			{
				"id":   1,
				"type": "scheduled_task",
				"key":  "daily",
				"name": "Daily",
				"content": map[string]any{
					"schemaVersion": 1,
					"kind":          "scheduled_task",
					"format":        "task/openclaw-cron@v1",
					"config": map[string]any{
						"name":          "daily",
						"enabled":       true,
						"schedule":      map[string]any{"kind": "cron", "expr": "0 9 * * *"},
						"sessionTarget": "isolated",
						"wakeMode":      "now",
						"payload":       map[string]any{"kind": "agentTurn", "message": "brief"},
						"delivery":      map[string]any{"mode": "announce", "channel": "last"},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	req := CreateGatewayRequest{
		InstanceID:  8,
		UserID:      1,
		UID:         uid,
		GID:         gid,
		Environment: map[string]string{"CLAWMANAGER_OPENCLAW_SCHEDULED_TASKS_JSON": string(raw)},
	}
	cfg := Config{
		GatewayAuthMode: "trusted-proxy",
		PublicOrigin:    "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001",
		AllowedOrigins:  []string{"http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001"},
		TrustedProxies:  []string{"127.0.0.1"},
		LLMBaseURL:      "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001/api/v1/gateway/llm",
		LLMAPIKey:       "runtime-llm-token",
		LLMAPIKeySet:    true,
		LLMModelIDs:     []string{"gpt-5.5"},
	}
	if err := WriteGatewayConfig(cfg, req, workspace); err != nil {
		t.Fatalf("WriteGatewayConfig() error = %v", err)
	}

	storePath := filepath.Join(workspace, "home", ".openclaw", "cron", "jobs.json")
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("stat jobs.json: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type %T", info.Sys())
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("jobs.json owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
	}
}
