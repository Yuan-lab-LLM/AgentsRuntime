package opencode

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func (p Profile) RunCommand(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "llm-config" {
		return false, nil
	}
	flags := flag.NewFlagSet("llm-config", flag.ContinueOnError)
	format := flags.String("format", "canonical", "output format")
	flags.String("fallback-provider", "auto", "canonical provider")
	flags.String("default-model", "", "canonical model")
	if err := flags.Parse(args[1:]); err != nil {
		return true, err
	}
	if strings.ToLower(strings.TrimSpace(*format)) != "opencode" {
		return false, nil
	}
	if flags.NArg() != 0 {
		return true, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	settings, err := LoadLLMSettingsFromEnv()
	if err != nil {
		return true, err
	}
	raw, err := RenderConfig(settings)
	if err != nil {
		return true, err
	}
	_, err = os.Stdout.Write(raw)
	return true, err
}
