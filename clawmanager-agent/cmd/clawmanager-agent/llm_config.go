package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/internal/llmconfig"
	"github.com/iamlovingit/clawmanager-agent/internal/runtime/opencode"
)

const (
	llmConfigFormatCanonical = "canonical"
	llmConfigFormatOpenCode  = "opencode"
)

func runLLMConfigCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("llm-config", flag.ContinueOnError)
	format := flags.String("format", llmConfigFormatCanonical, "output format: canonical or opencode")
	fallbackProvider := flags.String("fallback-provider", "auto", "provider for legacy unqualified model ids")
	defaultModel := flags.String("default-model", "", "model used when no model environment variable is set")
	mergeFile := flags.String("merge-file", "", "existing OpenCode JSON config to preserve non-managed settings from")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch strings.ToLower(strings.TrimSpace(*format)) {
	case llmConfigFormatCanonical:
		if *mergeFile != "" {
			return fmt.Errorf("merge-file requires opencode format")
		}
		return writeCanonicalLLMConfig(output, *fallbackProvider, *defaultModel)
	case llmConfigFormatOpenCode:
		return writeOpenCodeLLMConfig(output, *mergeFile)
	default:
		return fmt.Errorf("unsupported LLM config format %q", *format)
	}
}

func writeCanonicalLLMConfig(output io.Writer, fallbackProvider, defaultModel string) error {
	settings, err := llmconfig.LoadFromEnv(llmconfig.ResolveOptions{})
	if err != nil {
		return err
	}
	if len(settings.ModelIDs) == 0 && !settings.CatalogSynced && strings.TrimSpace(defaultModel) != "" {
		settings.ModelIDs = []string{strings.TrimSpace(defaultModel)}
	}

	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(settings.Canonical(fallbackProvider))
}

func writeOpenCodeLLMConfig(output io.Writer, mergePaths ...string) error {
	settings, err := opencode.LoadLLMSettingsFromEnv()
	if err != nil {
		return err
	}
	raw, err := opencode.RenderManagedConfig(settings, os.Getenv("OPENCODE_CONFIG_CONTENT"))
	if err != nil {
		return err
	}
	if len(mergePaths) > 0 && mergePaths[0] != "" {
		raw, err = opencode.MergeManagedFile(mergePaths[0], raw, settings)
		if err != nil {
			return err
		}
	}
	_, err = output.Write(raw)
	return err
}
