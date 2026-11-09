//go:build !linux && !darwin

package netguard

import (
	"context"
	"fmt"
	"os/exec"
)

func (h *Handle) wrap(*exec.Cmd, int, int, []uint32) error {
	return fmt.Errorf("netguard: network isolation requires Linux")
}
func (h *Handle) helperCommand(context.Context, ...string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("netguard: network isolation requires Linux")
}
