package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestManagedModelContextWrittenToHermes(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: []string{"vendor/model"}, LLMModelsQualified: true, LLMContextWindows: map[string]int{"vendor/model": 64000}}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(buildManagedYAML(cfg)), &doc); err != nil {
		t.Fatal(err)
	}
	model := doc["providers"].(map[string]any)["vendor"].(map[string]any)["models"].(map[string]any)["model"].(map[string]any)
	if model["context_length"] != 64000 {
		t.Fatalf("wrong context: %v", model)
	}
}

func TestHermesContextWindowPrecedenceAcrossConfigBranches(t *testing.T) {
	cases := []struct {
		name      string
		qualified bool
		ids       []string
		windows   map[string]int
		want      map[string]int
	}{
		{"qualified_before_bare_and_default", true, []string{"vendor/model", "other/model"}, map[string]int{"vendor/model": 64000, "other/model": 96000, "model": 128000}, map[string]int{"vendor/model": 64000, "other/model": 96000}},
		{"explicit_zero_does_not_inherit", true, []string{"vendor/model"}, map[string]int{"vendor/model": 0, "model": 128000}, map[string]int{"vendor/model": 0}},
		{"bare_model_fallback", true, []string{"vendor/model"}, map[string]int{"model": 64000}, map[string]int{"vendor/model": 64000}},
		{"default_fallback", true, []string{"vendor/model"}, nil, map[string]int{"vendor/model": 32000}},
		{"original_case_and_slash_in_model", true, []string{"Vendor/org/model"}, map[string]int{"Vendor/org/model": 64000, "org/model": 128000}, map[string]int{"Vendor/org/model": 64000}},
		{"unqualified_stable_identity", false, []string{"cm-deploy-v1-example"}, map[string]int{"cm-deploy-v1-example": 64000}, map[string]int{"clawmanager/cm-deploy-v1-example": 64000}},
	}
	for _, tc := range cases {
		for _, desktop := range []bool{false, true} {
			branch := "legacy-headless"
			if desktop {
				branch = "desktop-web"
			}
			t.Run(tc.name+"/"+branch, func(t *testing.T) {
				cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: tc.ids, LLMModelsQualified: tc.qualified, LLMContextWindow: 32000, LLMContextWindows: tc.windows}
				raw := []byte(buildManagedYAML(cfg))
				if desktop {
					var err error
					raw, err = mergeDesktopWebYAML(nil, cfg, "http://localhost", nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				var doc map[string]any
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				providers := doc["providers"].(map[string]any)
				for ref, want := range tc.want {
					provider, model, _ := strings.Cut(ref, "/")
					if desktop && tc.qualified {
						provider = "clawmanager-" + strings.ToLower(provider)
					}
					metadata := providers[provider].(map[string]any)["models"].(map[string]any)[model].(map[string]any)
					got, exists := metadata["context_length"]
					if want == 0 {
						if exists {
							t.Fatalf("%s: explicit unknown context inherited %v", ref, got)
						}
					} else if got != want {
						t.Fatalf("%s: context_length=%v, want %d", ref, got, want)
					}
				}
				firstProvider, firstModel := "clawmanager", tc.ids[0]
				if tc.qualified {
					firstProvider, firstModel, _ = strings.Cut(tc.ids[0], "/")
				}
				if desktop && tc.qualified {
					firstProvider = "clawmanager-" + strings.ToLower(firstProvider)
				}
				model := doc["model"].(map[string]any)
				if model["provider"] != firstProvider || model["default"] != firstModel {
					t.Fatalf("model routing changed: %v", model)
				}
			})
		}
	}
}

func TestDesktopWebQualifiedContextRefreshPreservesUserSettings(t *testing.T) {
	cfg := gateway.Config{LLMBaseURL: "http://gateway/v1", LLMModelIDs: []string{"vendor/model"}, LLMModelsQualified: true, LLMContextWindow: 32000, LLMContextWindows: map[string]int{"vendor/model": 64000}}
	raw := []byte(`custom_setting: keep
providers:
  clawmanager-vendor:
    base_url: http://gateway/v1
    key_env: OPENAI_API_KEY
    models:
      model:
        supports_tools: true
  mine:
    base_url: https://user.example/v1
    models:
      model:
        context_length: 12345
`)
	for _, window := range []int{64000, 16000} {
		cfg.LLMContextWindows["vendor/model"] = window
		var err error
		raw, err = mergeDesktopWebYAML(raw, cfg, "http://localhost", nil)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		providers := doc["providers"].(map[string]any)
		model := providers["clawmanager-vendor"].(map[string]any)["models"].(map[string]any)["model"].(map[string]any)
		if model["context_length"] != window || model["supports_tools"] != true {
			t.Fatalf("managed metadata not refreshed/preserved: %v", model)
		}
		foreign := providers["mine"].(map[string]any)["models"].(map[string]any)["model"].(map[string]any)
		if foreign["context_length"] != 12345 || doc["custom_setting"] != "keep" {
			t.Fatal("unrelated user settings changed")
		}
	}
}
