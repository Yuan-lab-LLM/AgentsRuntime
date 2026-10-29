package app

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	runtimeagent "github.com/iamlovingit/clawmanager-agent/agent"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/internal/instanceagent"
	"github.com/iamlovingit/clawmanager-agent/netrelay"
	"github.com/iamlovingit/clawmanager-agent/slots"
)

var Version = "dev"

type agentMode string

const (
	modeRuntimePod agentMode = "runtime-pod"
	modeInstance   agentMode = "instance"
	modeDisabled   agentMode = "disabled"
)

func Run(profile gateway.RuntimeProfile) {
	if len(os.Args) > 1 && os.Args[1] == netrelay.Command {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if _, err := netrelay.RunCommand(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
			_, _ = os.Stderr.WriteString("managed_network_relay_failed\n")
			os.Exit(2)
		}
		return
	}
	if handled, err := gateway.RunListenerProbe(os.Args[1:]); handled {
		if err != nil {
			// Never print arguments or /proc details from the instance-UID
			// helper. Its caller reports the corresponding safe error category.
			_, _ = os.Stderr.WriteString("managed_listener_verification_failed\n")
			os.Exit(2)
		}
		return
	}
	if handler, ok := profile.(interface{ RunCommand([]string) (bool, error) }); ok {
		handled, err := handler.RunCommand(os.Args[1:])
		if err != nil {
			log.Printf("runtime command: %v", err)
			os.Exit(2)
		}
		if handled {
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "llm-config" {
		if err := RunLLMConfigCommand(os.Args[2:], os.Stdout); err != nil {
			log.Printf("render LLM config: %v", err)
			os.Exit(2)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch selectMode() {
	case modeRuntimePod:
		cfg, err := runtimeagent.LoadConfigFromEnv(profile)
		if err != nil {
			log.Fatalf("load runtime agent config: %v", err)
		}
		var runErr error
		if role := strings.TrimSpace(os.Getenv("RUNTIME_AGENT_ROLE")); role != "" {
			runErr = slots.Run(ctx, cfg, role)
		} else {
			runErr = runtimeagent.NewAgent(cfg).Run(ctx)
		}
		if err := runErr; err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("run runtime agent: %v", err)
		}
	case modeInstance:
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		cfg, err := instanceagent.LoadConfig(Version)
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
