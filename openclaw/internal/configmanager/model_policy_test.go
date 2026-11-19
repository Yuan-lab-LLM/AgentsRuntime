package configmanager

import (
	"reflect"
	"testing"
)

func TestLegacyModelPolicyPreservesSettingsAndManagedListSync(t *testing.T) {
	for _, allow := range [][]any{{"p/only"}, {}} {
		entries := map[string]any{"p/only": map[string]any{"alias": "Only", "params": map[string]any{"temperature": 0.2}}, "p/new": map[string]any{}}
		defaults := map[string]any{"models": entries, "modelPolicy": map[string]any{"allow": allow}}
		for i := 0; i < 2; i++ {
			migrateLegacyModelAllowlist(defaults)
			if len(mapValue(defaults["modelPolicy"])["allow"].([]any)) != 2 || !reflect.DeepEqual(defaults["models"], entries) {
				t.Fatal("new managed model missing or settings changed")
			}
		}
	}
}
