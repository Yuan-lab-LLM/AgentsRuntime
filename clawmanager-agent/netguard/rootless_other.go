//go:build !linux

package netguard

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

func PrepareRootless(context.Context, Config, Identity, int, int, []uint32, string) (*RootlessHandle, error) {
	return nil, errors.New("netguard: rootless namespaces require Linux")
}
func (*RootlessHandle) Wrap(*exec.Cmd, int, int, []uint32) error {
	return errors.New("netguard: rootless namespaces require Linux")
}
func (*RootlessHandle) HelperCommand(context.Context, []*os.File, ...string) (*exec.Cmd, error) {
	return nil, errors.New("netguard: rootless namespaces require Linux")
}
func RunRootlessCommand(_ context.Context, args []string, _ io.Reader, _ io.Writer) (bool, error) {
	if len(args) > 0 && args[0] == RootlessCommand {
		return true, errors.New("netguard: rootless namespaces require Linux")
	}
	return false, nil
}
