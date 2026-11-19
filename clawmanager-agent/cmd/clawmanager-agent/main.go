package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	runtimeagent "github.com/iamlovingit/clawmanager-agent/internal/agent"
	"github.com/iamlovingit/clawmanager-agent/internal/dshproxy"
	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/instanceagent"
)

var version = "dev"

type agentMode string

const (
	modeRuntimePod agentMode = "runtime-pod"
	modeInstance   agentMode = "instance"
	modeDisabled   agentMode = "disabled"
)

func main() {
	if handled, err := gateway.RunListenerProbe(os.Args[1:]); handled {
		if err != nil {
			// Never print arguments or /proc details from the instance-UID
			// helper. Its caller reports the corresponding safe error category.
			_, _ = os.Stderr.WriteString("managed_listener_verification_failed\n")
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "llm-config" {
		if err := runLLMConfigCommand(os.Args[2:], os.Stdout); err != nil {
			log.Printf("render LLM config: %v", err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "dsh-web-proxy" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := dshproxy.RunCommand(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("run DSH web proxy: %v", err)
			os.Exit(dshproxy.CommandExitCode(err))
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch selectMode() {
	case modeRuntimePod:
		cfg, err := runtimeagent.LoadConfigFromEnv()
		if err != nil {
			log.Fatalf("load runtime agent config: %v", err)
		}
		if err := runtimeagent.NewAgent(cfg).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("run runtime agent: %v", err)
		}
	case modeInstance:
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		cfg, err := instanceagent.LoadConfig(version)
		if err != nil {
			logger.Error("load instance agent config", "error", err)
			os.Exit(2)
		}
		if !cfg.Enabled {
			logger.Info("ClawManager instance agent disabled")
			return
		}
		if err := instanceagent.New(cfg, logger).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("run instance agent", "error", err)
			os.Exit(1)
		}
	case modeDisabled:
		log.Print("ClawManager shared agent disabled")
	}
}

func selectMode() agentMode {
	instanceRequested := instanceAgentRequested()
	// Lite runtime-pod only when Pro instance credentials are not present.
	if runtimeagent.RuntimeAgentModeEnabled() && !instanceRequested {
		return modeRuntimePod
	}
	if instanceRequested && managedDesktopInstanceAllowed() {
		return modeInstance
	}
	return modeDisabled
}

func instanceAgentRequested() bool {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_ENABLED")), "true") {
		return false
	}
	if strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_INSTANCE_ID")) == "" {
		return false
	}
	if strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_BOOTSTRAP_TOKEN")) == "" {
		return false
	}
	return true
}

// managedDesktopInstanceAllowed decides whether this image's clawmanager-agent
// should run Pro instance mode for managed coding-agent desktops.
// ClawManager uses CLAWMANAGER_RUNTIME_TYPE=desktop|gateway as a backend marker;
// that must not reject Pro instance mode.
func managedDesktopInstanceAllowed() bool {
	product := strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_AGENT_RUNTIME_TYPE")))
	if product == "" {
		backend := strings.ToLower(strings.TrimSpace(os.Getenv("CLAWMANAGER_RUNTIME_TYPE")))
		switch backend {
		case "desktop", "gateway":
			product = ""
		default:
			product = backend
		}
	}
	if product == "" {
		return true
	}
	switch product {
	case "hermes", "opencode":
		return true
	default:
		return false
	}
}
