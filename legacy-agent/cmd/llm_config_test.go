package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

func TestRunLLMConfigCommandWritesCanonicalConfigWithoutSecret(t *testing.T) {
	for _, name := range []string{
		"OPENAI_BASE_URL",
		"OPENAI_API_BASE",
		"OPENAI_API_KEY",
		"CLAWMANAGER_LLM_MODEL",
		"OPENAI_MODEL",
		"CLAWMANAGER_LLM_REASONING",
		"CLAWMANAGER_LLM_REASONING_CONTROL",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("CLAWMANAGER_LLM_BASE_URL", "https://gateway.example/v1/")
	t.Setenv("CLAWMANAGER_LLM_API_KEY", "do-not-render-this-secret")
	t.Setenv("CLAWMANAGER_LLM_PROVIDER_MODELS", `["auto/auto","deepseek/deepseek-v4"]`)

	var output bytes.Buffer
	if err := runLLMConfigCommand([]string{
		"--format", "canonical",
		"--fallback-provider", "clawmanager",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "do-not-render-this-secret") {
		t.Fatal("canonical output exposed the API key")
	}

	var config llmconfig.Canonical
	if err := json.Unmarshal(output.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.BaseURL != "https://gateway.example/v1" || config.APIKeySource != "CLAWMANAGER_LLM_API_KEY" {
		t.Fatalf("canonical connection = %#v", config)
	}
	if config.DefaultProvider != "auto" || config.DefaultModel != "auto" || len(config.Providers) != 2 {
		t.Fatalf("canonical models = %#v", config)
	}
}
