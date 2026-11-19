package managedmodelrefs

import (
	"encoding/json"
	"reflect"
	"testing"
)

func snapshot(t *testing.T, config map[string]any) string {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func fixtureProvider(key string, ids ...string) map[string]any {
	models := []any{}
	for _, id := range ids {
		models = append(models, map[string]any{"id": id, "contextWindow": 8192})
	}
	return map[string]any{"baseUrl": "http://gateway/v1", "apiKey": key, "models": models}
}
func TestNormalizeAgentAliasConflictIsAtomic(t *testing.T) {
	name := "model (vendor)"
	entries := map[string]any{"p/" + name: map[string]any{"alias": "old"}, "p/" + ModelID(name): map[string]any{"alias": "other"}}
	for _, agentList := range []bool{false, true} {
		agents := map[string]any{"defaults": map[string]any{"models": entries}}
		if agentList {
			agents = map[string]any{"list": []any{map[string]any{"id": "a", "models": entries}}}
		}
		config := map[string]any{"models": map[string]any{"providers": map[string]any{"p": fixtureProvider("owner", name)}}, "agents": agents}
		before := snapshot(t, config)
		if _, err := Normalize(config, "http://gateway/v1", "owner"); err == nil || snapshot(t, config) != before {
			t.Fatal("alias conflict lost settings")
		}
	}
}

func TestNormalizeBatchConflictsAreAtomic(t *testing.T) {
	for _, sameURL := range []bool{false, true} {
		t.Run(map[bool]string{true: "same-url", false: "other-url"}[sameURL], func(t *testing.T) {
			target := fixtureProvider("other", "existing")
			if !sameURL {
				target["baseUrl"] = "http://external/v1"
			}
			providers := map[string]any{"a first": fixtureProvider("owner", "a model"), "z last": fixtureProvider("owner", "z model"), ProviderID("z last"): target}
			config := map[string]any{"models": map[string]any{"providers": providers}}
			before := snapshot(t, config)
			changed, err := Normalize(config, "http://gateway/v1", "owner")
			if err == nil || changed || snapshot(t, config) != before {
				t.Fatal("conflict mutated config", err)
			}
		})
	}
	config := map[string]any{"models": map[string]any{"providers": map[string]any{"p": fixtureProvider("owner", "name (vendor)", ModelID("name (vendor)"))}}}
	before := snapshot(t, config)
	if _, err := Normalize(config, "http://gateway/v1", "owner"); err == nil || snapshot(t, config) != before {
		t.Fatal("duplicate normalized model accepted or modified")
	}
}
func TestNormalizeRequiresMatchingLiteralCredential(t *testing.T) {
	foreign := fixtureProvider("other", "model (private)")
	foreignRef := "foreign provider/model (private)"
	defaults := map[string]any{"model": foreignRef, "modelPolicy": map[string]any{"allow": []any{foreignRef}}}
	config := map[string]any{"models": map[string]any{"providers": map[string]any{"foreign provider": foreign}}, "agents": map[string]any{"defaults": defaults}}
	for _, key := range []any{"other", map[string]any{"source": "env", "id": "KEY"}} {
		foreign["apiKey"] = key
		before := snapshot(t, config)
		if changed, err := Normalize(config, "http://gateway/v1", "owner"); err != nil || changed || snapshot(t, config) != before {
			t.Fatal("foreign credential modified", err)
		}
	}
}
func TestPrepareReusesCanonicalAndPreservesMetadata(t *testing.T) {
	old := "name (vendor)"
	provider := fixtureProvider("owner", ModelID(old))
	config := map[string]any{"models": map[string]any{"providers": map[string]any{ProviderID(old): provider}}}
	if err := PrepareProviders(config, []string{old}, "http://gateway/v1/", "owner"); err != nil {
		t.Fatal(err)
	}
	providers := object(object(config["models"])["providers"])
	if !reflect.DeepEqual(providers[old], provider) || len(providers) != 1 {
		t.Fatal("canonical metadata lost")
	}
	if _, err := Normalize(config, "http://gateway/v1", "owner"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providers[ProviderID(old)], provider) {
		t.Fatal("round trip lost provider")
	}
}
func TestPrepareBatchConflictDoesNotMoveEarlierProvider(t *testing.T) {
	for _, key := range []string{"owner", "foreign"} {
		a, b := "a old", "z old"
		providers := map[string]any{ProviderID(a): fixtureProvider("owner", "a"), ProviderID(b): fixtureProvider(key, "b")}
		if key == "owner" {
			providers[b] = fixtureProvider(key, "conflicting duplicate")
		}
		config := map[string]any{"models": map[string]any{"providers": providers}}
		before := snapshot(t, config)
		if err := PrepareProviders(config, []string{a, b}, "http://gateway/v1", "owner"); err == nil || snapshot(t, config) != before {
			t.Fatal("batch conflict changed config")
		}
	}
}
func TestPickerMergePreservesCustomAndRemovesStaleManaged(t *testing.T) {
	config := map[string]any{"models": map[string]any{"providers": map[string]any{"retired": fixtureProvider("owner", "old"), "custom": fixtureProvider("foreign", "custom")}}}
	owned := ManagedProviderNames(config, []string{"auto"}, "http://gateway/v1", "owner")
	custom := map[string]any{"alias": "my custom", "params": map[string]any{"temperature": 0.2}}
	entries := map[string]any{"retired/old": map[string]any{}, "auto/removed": map[string]any{}, "custom/custom": custom}
	out := MergeAgentEntries(entries, map[string]any{"auto/new": map[string]any{}}, owned)
	if len(out) != 2 || !reflect.DeepEqual(out["custom/custom"], custom) {
		t.Fatal("custom entry lost or stale grant survived", out)
	}
	if len(entries) != 3 {
		t.Fatal("source entries changed")
	}
}
