package profiles

import (
	hermes "clawmanager-hermes-image/lite"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/agent"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/runtime/generic"
	deepseek "github.com/iamlovingit/clawmanager-deepseek-harness-image/lite"
	"github.com/iamlovingit/clawmanager-legacy-agent/windowsvm"
	"github.com/iamlovingit/clawmanager-openclaw-image/lite"
	opencode "github.com/iamlovingit/clawmanager-opencode-image/lite"
	"os"
	"strings"
)

func Resolve() (gateway.RuntimeProfile, error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_RUNTIME_TYPE")))
	switch kind {
	case "", "openclaw":
		return openclaw.NewProfile("openclaw"), nil
	case "openclaw-shell":
		return openclaw.NewProfile(kind), nil
	case "hermes":
		return hermes.NewProfile(kind), nil
	case "opencode":
		return opencode.NewProfile(kind), nil
	case "deepseek-harness":
		return deepseek.NewProfile(kind), nil
	case "windows-vm":
		return windowsvm.NewProfile(kind), nil
	default:
		if strings.TrimSpace(os.Getenv("RUNTIME_GATEWAY_COMMAND")) == "" {
			return nil, fmt.Errorf("unknown CLAWMANAGER_RUNTIME_TYPE %q requires RUNTIME_GATEWAY_COMMAND", kind)
		}
		return generic.NewProfile(kind), nil
	}
}
func LoadConfigFromEnv() (agent.Config, error) {
	p, err := Resolve()
	if err != nil {
		return agent.Config{}, err
	}
	return agent.LoadConfigFromEnv(p)
}
