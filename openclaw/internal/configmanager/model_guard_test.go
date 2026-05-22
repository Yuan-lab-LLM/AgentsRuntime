package configmanager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

func TestEnforceModelBaselineRestoresChangedModel(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{OpenClawConfigPath: configPath}, nil, nil)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	defaults := nestedMapForTest(t, cfg, "agents", "defaults")
	defaults["model"] = map[string]any{"primary": "auto/hijacked-model"}
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("expected model baseline restore to run")
	}

	restoredCfg := readConfigForTest(t, configPath)
	model := nestedMapForTest(t, restoredCfg, "agents", "defaults", "model")
	if got := model["primary"]; got != "auto/legacy-model" {
		t.Fatalf("expected primary model to be restored, got %#v", got)
	}
}

func TestEnforceModelBaselineRestoresModelsProvidersChanges(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{OpenClawConfigPath: configPath}, nil, nil)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	auto := nestedMapForTest(t, cfg, "models", "providers", "auto")
	auto["baseUrl"] = "http:/"
	auto["apiKey"] = "changed-api-key"
	auto["models"] = []any{
		map[string]any{"id": "hijacked-model"},
	}
	providers := nestedMapForTest(t, cfg, "models", "providers")
	providers["new-provider"] = map[string]any{
		"api": "openai-completions",
	}
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("expected model baseline restore to run")
	}

	finalCfg := readConfigForTest(t, configPath)
	finalAuto := nestedMapForTest(t, finalCfg, "models", "providers", "auto")
	if got := finalAuto["baseUrl"]; got != "https://legacy.example/v1" {
		t.Fatalf("expected baseUrl to be restored, got %#v", got)
	}
	if got := finalAuto["apiKey"]; got != "legacy-api-key" {
		t.Fatalf("expected apiKey to be restored, got %#v", got)
	}
	models, ok := finalAuto["models"].([]any)
	if !ok || len(models) == 0 {
		t.Fatalf("expected auto models array to be restored, got %#v", finalAuto["models"])
	}
	first, ok := models[0].(map[string]any)
	if !ok || first["id"] != "legacy-model" {
		t.Fatalf("expected first auto model id to be legacy-model, got %#v", first)
	}
	finalProviders := nestedMapForTest(t, finalCfg, "models", "providers")
	if _, ok := finalProviders["new-provider"]; ok {
		t.Fatalf("expected injected provider to be removed, still have %#v", finalProviders["new-provider"])
	}
}

func TestEnforceModelBaselineAllowsCosmeticModelEdits(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{OpenClawConfigPath: configPath}, nil, nil)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	auto := nestedMapForTest(t, cfg, "models", "providers", "auto")
	models, ok := auto["models"].([]any)
	if !ok || len(models) == 0 {
		t.Fatal("expected auto models array")
	}
	first, ok := models[0].(map[string]any)
	if !ok {
		t.Fatal("expected model object")
	}
	first["name"] = "User Renamed Model"
	first["contextWindow"] = 128000
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if restored {
		t.Fatal("expected cosmetic-only edits not to trigger model guard write")
	}

	finalCfg := readConfigForTest(t, configPath)
	finalAuto := nestedMapForTest(t, finalCfg, "models", "providers", "auto")
	finalModels := finalAuto["models"].([]any)
	finalFirst := finalModels[0].(map[string]any)
	if got := finalFirst["name"]; got != "User Renamed Model" {
		t.Fatalf("expected name to remain user-edited, got %#v", got)
	}
}

func TestEnforceModelBaselineDoesNotTouchChannels(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{OpenClawConfigPath: configPath}, nil, nil)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	cfg["channels"] = map[string]any{
		"dingtalk": map[string]any{"enabled": true},
	}
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if restored {
		t.Fatal("expected channel-only change to bypass model guard")
	}
}

func TestApplyRevisionRefreshesModelBaseline(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "active", "openclaw.json")
	stateDir := filepath.Join(root, "state")

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(sampleOpenClawConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	revisionConfig := strings.ReplaceAll(sampleOpenClawConfig, "legacy-model", "revision-model")
	manager := New(appconfig.Config{
		AgentDataDir:       filepath.Join(root, "agent-data"),
		OpenClawConfigPath: configPath,
	}, stubRevisionClient{
		resp: protocol.ConfigRevisionResponse{
			Content: []byte(revisionConfig),
		},
	}, st)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyRevision(context.Background(), "43"); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	defaults := nestedMapForTest(t, cfg, "agents", "defaults")
	defaults["model"] = map[string]any{"primary": "auto/hijacked-model"}
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("expected model baseline restore to run")
	}

	finalCfg := readConfigForTest(t, configPath)
	model := nestedMapForTest(t, finalCfg, "agents", "defaults", "model")
	if got := model["primary"]; got != "auto/revision-model" {
		t.Fatalf("expected revision model baseline to be restored, got %#v", got)
	}
}

func TestEnforceModelBaselineRemovesInjectedModelWhenBaselineMissing(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "openclaw.json")
	source := `{"agents":{"defaults":{"models":{"auto/a":{}}}}}`
	if err := os.WriteFile(configPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := New(appconfig.Config{OpenClawConfigPath: configPath}, nil, nil)
	if err := manager.CaptureModelBaseline(); err != nil {
		t.Fatal(err)
	}

	cfg := readConfigForTest(t, configPath)
	defaults := nestedMapForTest(t, cfg, "agents", "defaults")
	defaults["model"] = map[string]any{"primary": "auto/added"}
	writeConfigForTest(t, configPath, cfg)

	restored, err := manager.EnforceModelBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("expected model baseline restore to run")
	}

	finalCfg := readConfigForTest(t, configPath)
	finalDefaults := nestedMapForTest(t, finalCfg, "agents", "defaults")
	if _, ok := finalDefaults["model"]; ok {
		t.Fatalf("expected injected model field to be removed, got %#v", finalDefaults["model"])
	}
}

func writeConfigForTest(t *testing.T, path string, cfg map[string]any) {
	t.Helper()
	payload, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}
