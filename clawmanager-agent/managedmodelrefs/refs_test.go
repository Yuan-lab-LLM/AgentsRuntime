package managedmodelrefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAliasWireContract(t *testing.T) {
	if got := Alias(" abc "); got != "cm-model-v1-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(got)
	}
}

func TestNormalizeReportedModelsAndSelections(t *testing.T) {
	const url = "http://gateway/api/v1/gateway/llm"
	names := []string{"deepseek-v4.1-flash-public (openai)", "glm-5.3-public (openai)", "中文 模型"}
	providers := map[string]any{}
	allow, deny := []any{"auto/auto", "custom/model"}, []any{}
	legacy := map[string]any{}
	for _, name := range names {
		providers[name] = map[string]any{"baseUrl": url, "apiKey": "preserve", "models": []any{map[string]any{"id": name, "name": name, "reasoning": true}}}
		ref := name + "/" + name
		allow = append(allow, ref)
		deny = append(deny, ref, name+"/*")
		legacy[ref] = map[string]any{"alias": "my alias"}
	}
	custom := map[string]any{"baseUrl": "https://external.invalid", "models": []any{map[string]any{"id": "custom model"}}}
	providers["custom"] = custom
	original := names[0] + "/" + names[0]
	selection := map[string]any{"primary": original, "fallbacks": []any{names[1] + "/" + names[1], "custom/model"}}
	defaults := map[string]any{"models": legacy, "model": selection, "modelPolicy": map[string]any{"allow": allow, "deny": deny}}
	agent := map[string]any{"model": original, "imageModel": original, "heartbeat": map[string]any{"model": original}}
	config := map[string]any{"models": map[string]any{"providers": providers}, "agents": map[string]any{"defaults": defaults, "list": []any{agent}}, "other": map[string]any{"primary": original}}
	changed, err := Normalize(config, url)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	for _, name := range names {
		p := object(providers[providerAlias(name)])
		if p == nil || p["apiKey"] != "preserve" {
			t.Fatal("provider/credentials lost")
		}
		m := object(p["models"].([]any)[0])
		if m["id"] != Alias(name) || m["name"] != name || m["reasoning"] != true {
			t.Fatalf("model lost: %#v", m)
		}
		if _, exists := providers[name]; exists {
			t.Fatal("old provider remains")
		}
		if object(legacy[providerAlias(name)+"/"+Alias(name)])["alias"] != "my alias" {
			t.Fatal("legacy alias lost")
		}
	}
	expected := providerAlias(names[0]) + "/" + Alias(names[0])
	if selection["primary"] != expected || agent["model"] != expected || agent["imageModel"] != expected || object(agent["heartbeat"])["model"] != expected {
		t.Fatal("selection not migrated")
	}
	if object(config["other"])["primary"] != original {
		t.Fatal("unrelated setting modified")
	}
	if !reflect.DeepEqual(providers["custom"], custom) {
		t.Fatal("external provider modified")
	}
	policy := object(defaults["modelPolicy"])
	if policy["allow"].([]any)[0] != "auto/auto" || policy["allow"].([]any)[1] != "custom/model" {
		t.Fatal("unrelated grants changed")
	}
	if policy["deny"].([]any)[0] != expected || policy["deny"].([]any)[1] != providerAlias(names[0])+"/*" {
		t.Fatal("deny policy changed meaning")
	}
	before, _ := json.Marshal(config)
	changed, err = Normalize(config, url)
	after, _ := json.Marshal(config)
	if err != nil || changed || string(before) != string(after) {
		t.Fatal("migration is not idempotent")
	}
}

func TestNormalizeNoOverridesAndProviderCollision(t *testing.T) {
	bad := "model (vendor)"
	providers := map[string]any{bad: map[string]any{"baseUrl": "http://gateway", "models": []any{map[string]any{"id": bad}}}}
	config := map[string]any{"models": map[string]any{"providers": providers}}
	if changed, err := Normalize(config, ""); changed || err != nil {
		t.Fatal("unmanaged config modified")
	}
	providers[providerAlias(bad)] = map[string]any{"baseUrl": "https://custom.invalid"}
	if _, err := Normalize(config, "http://gateway"); err == nil {
		t.Fatal("custom provider overwritten")
	}
}

func TestBackupOncePreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openclaw.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BackupOnce(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BackupOnce(path); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path + ".model-ref-v1.bak")
	if err != nil || string(saved) != "original" {
		t.Fatalf("%s %v", saved, err)
	}
}
