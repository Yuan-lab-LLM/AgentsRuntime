package main

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/agent"
	"github.com/iamlovingit/clawmanager-agent/app"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-deepseek-harness-image/dshproxy"
	"github.com/iamlovingit/clawmanager-legacy-agent/profiles"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "llm-config" {
		if err := runLLMConfigCommand(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "dsh-web-proxy" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := dshproxy.RunCommand(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
		return
	}
	var profile gateway.RuntimeProfile
	// Pro instance mode takes precedence when both credential sets are present.
	instance := strings.EqualFold(strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_ENABLED")), "true") && strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_INSTANCE_ID")) != "" && strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN")) != ""
	if agent.RuntimeAgentModeEnabled() && !instance {
		var err error
		profile, err = profiles.Resolve()
		if err != nil {
			log.Fatal(err)
		}
	}
	app.Version = version
	app.Run(profile)
}
