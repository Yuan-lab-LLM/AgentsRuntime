package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iamlovingit/clawmanager-openclaw-image/internal/bootstrap"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/browser"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	configmanager "github.com/iamlovingit/clawmanager-openclaw-image/internal/configmanager"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/supervisor"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// The shared Agent owns all member Gateway lifecycles in a Lite pool.
	// Match its service activation contract before Desktop bootstrap touches
	// /config or starts a second, unrelated default Gateway.
	if sharedRuntimeAgentEnabled() {
		log.Print("Shared Runtime Agent owns Gateway lifecycle; Desktop service is inactive")
		<-ctx.Done()
		return
	}

	cfg, err := appconfig.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := bootstrap.Run(cfg); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	if !cfg.Enabled {
		if err := normalizeRuntimeConfig(cfg); err != nil {
			log.Fatalf("normalize openclaw config: %v", err)
		}
		log.Printf("ClawManager agent disabled; running %s without control plane integration", cfg.RuntimeName)
		if err := runRuntime(ctx, cfg.OpenClawCommand); err != nil {
			log.Fatalf("run runtime: %v", err)
		}
		return
	}

	s, err := supervisor.New(cfg)
	if err != nil {
		log.Fatalf("init supervisor: %v", err)
	}

	go browser.Launch(ctx, cfg)

	if err := s.Run(ctx); err != nil {
		log.Fatalf("run supervisor: %v", err)
	}
}

func sharedRuntimeAgentEnabled() bool {
	return strings.TrimSpace(os.Getenv("RUNTIME_AGENT_CONTROL_TOKEN")) != "" ||
		strings.TrimSpace(os.Getenv("RUNTIME_AGENT_REPORT_TOKEN")) != ""
}

func normalizeRuntimeConfig(cfg appconfig.Config) error {
	return configmanager.New(cfg, nil, nil).NormalizeActiveConfig()
}

func runRuntime(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
