package llmconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestResolveGatewayPrefersQualifiedProviderModels(t *testing.T) {
	settings, err := ResolveGateway(gateway.Config{}, gateway.CreateGatewayRequest{
		Environment: map[string]string{
			ProviderModelsEnv: `["auto/auto","deepseek/deepseek-v4","deepseek/deepseek-v4"]`,
			LegacyModelsEnv:   `["auto","legacy"]`,
		},
	}, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.ModelsQualified || settings.ModelSource != ProviderModelsEnv {
		t.Fatalf("qualified source = %t, %q", settings.ModelsQualified, settings.ModelSource)
	}
	want := []ProviderModels{
		{ProviderID: "auto", ModelIDs: []string{"auto"}},
		{ProviderID: "deepseek", ModelIDs: []string{"deepseek-v4"}},
	}
	if got := GroupModelRefs(settings.ModelRefs("auto")); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider groups = %#v, want %#v", got, want)
	}
}

func TestResolveGatewayKeepsSlashInLegacyModelID(t *testing.T) {
	settings, err := ResolveGateway(gateway.Config{}, gateway.CreateGatewayRequest{
		Environment: map[string]string{LegacyModelsEnv: `["meta-llama/Llama-3.1"]`},
	}, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs := settings.ModelRefs("auto")
	if len(refs) != 1 || refs[0].ProviderID != "auto" || refs[0].ModelID != "meta-llama/Llama-3.1" || refs[0].Qualified != "auto/meta-llama/Llama-3.1" {
		t.Fatalf("legacy refs = %#v", refs)
	}
}

func TestResolveGatewayFallsBackPastBlankPreferredModelEnv(t *testing.T) {
	settings, err := ResolveGateway(gateway.Config{}, gateway.CreateGatewayRequest{
		Environment: map[string]string{
			ProviderModelsEnv: "   ",
			LegacyModelsEnv:   `["legacy"]`,
		},
	}, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ModelsQualified || !reflect.DeepEqual(settings.ModelIDs, []string{"legacy"}) {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestResolveGatewayReportsActualInvalidEnvironmentName(t *testing.T) {
	_, err := ResolveGateway(gateway.Config{}, gateway.CreateGatewayRequest{
		Environment: map[string]string{ProviderModelsEnv: `[`},
	}, ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), ProviderModelsEnv) {
		t.Fatalf("error = %v, want %s", err, ProviderModelsEnv)
	}
}

func TestParseModelIDsSupportsObjectEntries(t *testing.T) {
	models, err := parseModelIDs(`[{"id":"first"},{"model":"second"}]`, LegacyModelsEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"first", "second"}) {
		t.Fatalf("models = %#v", models)
	}
}

func TestLoadFromEnvUsesOpenAIModelFallback(t *testing.T) {
	t.Setenv(ProviderModelsEnv, "")
	t.Setenv(LegacyModelsEnv, "")
	t.Setenv("OPENAI_MODEL", "gpt-5.5")
	settings, err := LoadFromEnv(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ModelSource != "OPENAI_MODEL" || !reflect.DeepEqual(settings.ModelIDs, []string{"gpt-5.5"}) {
		t.Fatalf("settings = %#v", settings)
	}
}
