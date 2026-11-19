package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
	"github.com/iamlovingit/clawmanager-agent/internal/runtime/opencode"
)

func TestOpenCodeProCLIAndLiteUseIdenticalMergedConfig(t *testing.T) {
	content := `{"$schema":"https://opencode.ai/config.json","model":"clawmanager/auto","provider":{"clawmanager":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"{env:CLAWMANAGER_LLM_BASE_URL}","apiKey":"{env:CLAWMANAGER_LLM_API_KEY}"},"models":{"auto":{"name":"auto"}}}}}`
	values := map[string]string{"CLAWMANAGER_LLM_BASE_URL": "https://gateway.example/v1", "CLAWMANAGER_LLM_API_KEY": "never-render-secret", "CLAWMANAGER_LLM_PROVIDER_MODELS": `["auto/auto","legacy/m"]`, "OPENCODE_CONFIG_CONTENT": content}
	for key, value := range values {
		t.Setenv(key, value)
	}
	workspace := t.TempDir()
	config := filepath.Join(workspace, "home", ".opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o750); err != nil {
		t.Fatal(err)
	}
	original := `{"model":"user/u","mcp":{"user-tool":{"type":"local","command":["test"]}},"provider":{"user":{"models":{"u":{}},"options":{"baseURL":"https://user.invalid","apiKey":"user-key"}}}}`
	if err := os.WriteFile(config, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runLLMConfigCommand([]string{"--format", "opencode", "--merge-file", config}, &output); err != nil {
		t.Fatal(err)
	}
	if actual, _ := os.ReadFile(config); string(actual) != original {
		t.Fatal("CLI merge changed its input file")
	}
	req := gateway.CreateGatewayRequest{UID: os.Getuid(), GID: os.Getgid(), Environment: values}
	if err := opencode.WriteGatewayConfig(gateway.Config{}, req, workspace); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(actual, output.Bytes()) {
		t.Fatalf("Lite and Pro config differ: %v", err)
	}
	if strings.Contains(output.String(), "never-render-secret") || strings.Contains(output.String(), "legacy/m") {
		t.Fatal("stale provider or credential leaked")
	}
	output.Reset()
	if err := runLLMConfigCommand([]string{"--format", "canonical", "--merge-file", config}, &output); err == nil {
		t.Fatal("merge-file accepted for unrelated output format")
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", "invalid")
	if err := runLLMConfigCommand([]string{"--format", "opencode", "--merge-file", config}, &output); err == nil || output.Len() != 0 {
		t.Fatal("invalid config produced partial output")
	}
}

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
