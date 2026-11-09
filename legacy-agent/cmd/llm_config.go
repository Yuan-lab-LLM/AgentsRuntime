package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/app"
	"github.com/iamlovingit/clawmanager-opencode-image/lite"
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch strings.ToLower(strings.TrimSpace(*format)) {
	case llmConfigFormatCanonical:
		return app.RunLLMConfigCommand([]string{"--fallback-provider", *fallbackProvider, "--default-model", *defaultModel}, output)
	case llmConfigFormatOpenCode:
		return writeOpenCodeLLMConfig(output)
	default:
		return fmt.Errorf("unsupported LLM config format %q", *format)
	}
}

func writeOpenCodeLLMConfig(output io.Writer) error {
	settings, err := opencode.LoadLLMSettingsFromEnv()
	if err != nil {
		return err
	}
	raw, err := opencode.RenderConfig(settings)
	if err != nil {
		return err
	}
	_, err = output.Write(raw)
	return err
}
