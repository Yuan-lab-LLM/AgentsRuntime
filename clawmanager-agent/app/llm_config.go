package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/llmconfig"
)

const (
	llmConfigFormatCanonical = "canonical"
	llmConfigFormatOpenCode  = "opencode"
)

func RunLLMConfigCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("llm-config", flag.ContinueOnError)
	format := flags.String("format", llmConfigFormatCanonical, "output format: canonical or opencode")
	fallbackProvider := flags.String("fallback-provider", "auto", "provider for legacy unqualified model ids")
	defaultModel := flags.String("default-model", "", "model used when no model environment variable is set")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch strings.ToLower(strings.TrimSpace(*format)) {
	case llmConfigFormatCanonical:
		return writeCanonicalLLMConfig(output, *fallbackProvider, *defaultModel)
	default:
		return fmt.Errorf("unsupported LLM config format %q", *format)
	}
}

func writeCanonicalLLMConfig(output io.Writer, fallbackProvider, defaultModel string) error {
	settings, err := llmconfig.LoadFromEnv(llmconfig.ResolveOptions{})
	if err != nil {
		return err
	}
	if len(settings.ModelIDs) == 0 && strings.TrimSpace(defaultModel) != "" {
		settings.ModelIDs = []string{strings.TrimSpace(defaultModel)}
	}

	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(settings.Canonical(fallbackProvider))
}
