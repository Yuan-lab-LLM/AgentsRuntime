package scheduledtasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyOpenClawFromEnvUpsertsManagedJobs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	openclawHome := filepath.Join(dir, ".openclaw")

	payload := map[string]any{
		"schemaVersion": 1,
		"items": []map[string]any{
			{
				"id":   7,
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
	env := map[string]string{EnvOpenClaw: string(raw)}
	result := ApplyOpenClawFromEnv(openclawHome, func(key string) string { return env[key] })
	if result.Error != "" {
		t.Fatalf("apply error: %s", result.Error)
	}
	if result.JobCount != 1 {
		t.Fatalf("job count = %d", result.JobCount)
	}
	store, err := loadStore(OpenClawCronStorePath(openclawHome))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Jobs) != 1 || store.Jobs[0].ID != "cm-st-7" {
		t.Fatalf("unexpected jobs: %+v", store.Jobs)
	}
	if !strings.Contains(string(store.Jobs[0].Delivery), `"mode"`) ||
		!strings.Contains(string(store.Jobs[0].Delivery), `announce`) {
		t.Fatalf("delivery not preserved: %s", string(store.Jobs[0].Delivery))
	}

	second := ApplyOpenClawFromEnv(openclawHome, func(key string) string { return env[key] })
	if second.Error != "" {
		t.Fatalf("second apply error: %s", second.Error)
	}
	if !second.Skipped {
		t.Fatal("expected second identical apply to skip")
	}
}

func TestApplyHermesFromEnvTranslatesWebhookAndAnnounce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hermesHome := filepath.Join(dir, ".hermes")

	payload := map[string]any{
		"schemaVersion": 1,
		"items": []map[string]any{
			{
				"id":   9,
				"type": "scheduled_task",
				"key":  "hook",
				"name": "Hook",
				"content": map[string]any{
					"schemaVersion": 1,
					"kind":          "scheduled_task",
					"format":        "task/openclaw-cron@v1",
					"config": map[string]any{
						"name":          "hook",
						"schedule":      map[string]any{"kind": "every", "everyMs": 120000},
						"sessionTarget": "isolated",
						"wakeMode":      "now",
						"payload":       map[string]any{"kind": "agentTurn", "message": "ping"},
						"delivery":      map[string]any{"mode": "webhook", "to": "https://example.com/h"},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{EnvHermes: string(raw)}
	result := ApplyHermesFromEnv(hermesHome, func(key string) string { return env[key] })
	if result.Error != "" {
		t.Fatalf("apply error: %s", result.Error)
	}
	file, err := loadHermesJobs(HermesCronStorePath(hermesHome))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Jobs) != 1 {
		t.Fatalf("jobs=%d", len(file.Jobs))
	}
	job := file.Jobs[0]
	if job.ID != "cm-st-9" {
		t.Fatalf("id=%s", job.ID)
	}
	if job.Deliver != "local" {
		t.Fatalf("deliver=%s", job.Deliver)
	}
	if job.Schedule["kind"] != "interval" {
		t.Fatalf("schedule=%v", job.Schedule)
	}
	if !strings.Contains(job.Prompt, "https://example.com/h") {
		t.Fatalf("webhook instruction missing: %s", job.Prompt)
	}
	webhookFile := filepath.Join(hermesHome, "cron", "webhooks", "cm-st-9.url")
	data, err := os.ReadFile(webhookFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "https://example.com/h" {
		t.Fatalf("webhook file=%q", string(data))
	}
	if len(result.IgnoredFields) == 0 {
		t.Fatal("expected ignored_fields for hermes translation")
	}
	second := ApplyHermesFromEnv(hermesHome, func(key string) string { return env[key] })
	if second.Error != "" {
		t.Fatalf("second hermes apply error: %s", second.Error)
	}
	if !second.Skipped {
		t.Fatal("expected second identical hermes apply to skip")
	}
}
