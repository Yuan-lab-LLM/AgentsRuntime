package deepseek

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-deepseek-harness-image/dshproxy"
	"os"
	"os/signal"
	"syscall"
)

func (p Profile) RunCommand(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "dsh-web-proxy" {
		return false, nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := dshproxy.RunCommand(ctx, args[1:], os.Stdout, os.Stderr)
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return true, err
}
