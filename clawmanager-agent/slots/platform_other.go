//go:build !linux && !darwin

package slots

import (
	"fmt"
	"os"
	"os/exec"
)

func configureSandboxProcess(*exec.Cmd, int, int, []uint32) error {
	return fmt.Errorf("sandbox requires Linux")
}
func lockWorkspace(*os.File) error { return fmt.Errorf("workspace locks unsupported") }
func sandboxDirectoryGroup(string) (uint32, error) {
	return 0, fmt.Errorf("sandbox directory groups unsupported")
}
