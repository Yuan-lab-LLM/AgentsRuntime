//go:build !linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
)

func processLaunchUID(*exec.Cmd) uint32 { return 0 }

func signalProcessAsUID(*os.Process, uint32, os.Signal) error {
	return fmt.Errorf("scoped pinned signaling requires Linux")
}
