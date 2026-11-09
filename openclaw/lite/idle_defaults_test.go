package openclaw

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLiteIdleDefaultsAbsentExplicitAndIdempotent(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "explicit"}[explicit], func(t *testing.T) {
			config := map[string]any{"cron": map[string]any{"enabled": true}, "custom": "preserved"}
			if explicit {
				config["agents"] = map[string]any{"defaults": map[string]any{"heartbeat": map[string]any{"every": "15m"}}}
				config["plugins"] = map[string]any{"entries": map[string]any{"memory-core": map[string]any{"config": map[string]any{"dreaming": map[string]any{"enabled": true}}}}}
				config["skills"] = map[string]any{"workshop": map[string]any{"autonomous": map[string]any{"mode": "on"}}}
			}
			mergeOpenClawLiteDefaults(config, "2026.7.1")
			heartbeat := objectAt(t, objectAt(t, objectAt(t, config, "agents"), "defaults"), "heartbeat")
			dreaming := objectAt(t, objectAt(t, objectAt(t, objectAt(t, objectAt(t, config, "plugins"), "entries"), "memory-core"), "config"), "dreaming")
			autonomous := objectAt(t, objectAt(t, objectAt(t, config, "skills"), "workshop"), "autonomous")
			wantEvery, wantDreaming, wantMode := "0m", false, "off"
			if explicit {
				wantEvery, wantDreaming, wantMode = "15m", true, "on"
			}
			if heartbeat["every"] != wantEvery || dreaming["enabled"] != wantDreaming || autonomous["mode"] != wantMode {
				t.Fatalf("defaults/overrides: heartbeat=%v dreaming=%v autonomous=%v", heartbeat, dreaming, autonomous)
			}
			if config["custom"] != "preserved" || objectAt(t, config, "cron")["enabled"] != true {
				t.Fatal("existing configuration lost")
			}
			before, _ := json.Marshal(config)
			mergeOpenClawLiteDefaults(config, "2026.7.1")
			after, _ := json.Marshal(config)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("defaults not idempotent")
			}
		})
	}
}
