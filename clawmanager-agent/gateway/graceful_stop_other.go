//go:build !linux

package gateway

import (
	"context"
	"os/exec"
)

// Windows' existing stop is a force kill, not a graceful upgrade primitive.
func newGracefulGatewayStop(*exec.Cmd, <-chan struct{}) func(context.Context) error { return nil }
