package openclawcompat

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestModelPolicyMigrationKeepsSettingsAndManagedListSync(t *testing.T) {
	for _, allow := range [][]string{{"p/only"}, {}} {
		settings := map[string]any{"p/only": map[string]any{"alias": "saved", "params": map[string]any{"temperature": 0.2}}, "p/new": map[string]any{}}
		defaults := map[string]any{"models": settings, "modelPolicy": map[string]any{"allow": allow}}
		config := map[string]any{"agents": map[string]any{"defaults": defaults}}
		for i := 0; i < 2; i++ {
			Normalize81(config)
			updated := stringArray(objectValue(defaults["modelPolicy"])["allow"])
			if len(updated) != 2 {
				t.Fatal("new managed model missing", updated)
			}
			if !reflect.DeepEqual(defaults["models"], settings) {
				t.Fatal("settings lost")
			}
		}
	}
}
func TestModelPolicyMigrationCopiesLegacyRestrictionOnce(t *testing.T) {
	defaults := map[string]any{"models": map[string]any{"p/b": map[string]any{"alias": "B"}, "p/a": map[string]any{}}}
	config := map[string]any{"agents": map[string]any{"defaults": defaults}}
	Normalize81(config)
	if !reflect.DeepEqual(objectValue(defaults["modelPolicy"])["allow"], []string{"p/a", "p/b"}) {
		t.Fatal("legacy restriction not preserved")
	}
	before, _ := json.Marshal(config)
	Normalize81(config)
	after, _ := json.Marshal(config)
	if string(before) != string(after) {
		t.Fatal("not idempotent")
	}
}
