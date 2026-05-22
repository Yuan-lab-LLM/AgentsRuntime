package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/iamlovingit/clawmanager-openclaw-image/internal/bootstrap"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/browser"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/supervisor"
)

func main() {
	cfg, err := appconfig.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := bootstrap.Run(cfg); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !cfg.Enabled {
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
