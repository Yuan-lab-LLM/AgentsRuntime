//go:build unix

package scheduledtasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestApplyOpenClawFromEnvOwnsJobsFile(t *testing.T) {
	t.Parallel()
	uid := os.Getuid()
	gid := os.Getgid()
	if uid <= 0 || gid <= 0 {
		t.Skip("need positive uid/gid for ownership assertions")
	}

	openclawHome := filepath.Join(t.TempDir(), ".openclaw")
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
	result := ApplyOpenClawFromEnv(openclawHome, func(key string) string { return env[key] }, uid, gid)
	if result.Error != "" {
		t.Fatalf("apply error: %s", result.Error)
	}
	if result.Skipped {
		t.Fatal("expected first apply to write jobs, not skip")
	}

	storePath := OpenClawCronStorePath(openclawHome)
	assertOwnedMode(t, storePath, uid, gid, 0o640)
	assertOwnedMode(t, filepath.Dir(storePath), uid, gid, -1)
}

func TestApplyHermesFromEnvOwnsJobsAndWebhook(t *testing.T) {
	t.Parallel()
	uid := os.Getuid()
	gid := os.Getgid()
	if uid <= 0 || gid <= 0 {
		t.Skip("need positive uid/gid for ownership assertions")
	}

	hermesHome := filepath.Join(t.TempDir(), ".hermes")
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
	result := ApplyHermesFromEnv(hermesHome, func(key string) string { return env[key] }, uid, gid)
	if result.Error != "" {
		t.Fatalf("apply error: %s", result.Error)
	}
	if result.Skipped {
		t.Fatal("expected first hermes apply to write jobs, not skip")
	}

	storePath := HermesCronStorePath(hermesHome)
	assertOwnedMode(t, storePath, uid, gid, 0o640)

	hashPath := filepath.Join(hermesHome, "cron", "clawmanager-scheduled-tasks.sha256")
	assertOwnedMode(t, hashPath, uid, gid, 0o640)

	webhookPath := filepath.Join(hermesHome, "cron", "webhooks", "cm-st-9.url")
	assertOwnedMode(t, webhookPath, uid, gid, 0o640)
}

func assertOwnedMode(t *testing.T, path string, uid, gid, wantMode int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: unexpected Sys type %T", path, info.Sys())
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("owner %s = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
	}
	if wantMode >= 0 {
		got := info.Mode().Perm()
		if got != os.FileMode(wantMode) {
			t.Fatalf("mode %s = %o, want %o", path, got, wantMode)
		}
	}
}
