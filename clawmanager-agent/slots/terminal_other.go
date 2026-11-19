//go:build !linux

package slots

import (
	"fmt"
	"os"
	"os/exec"
)

func openTerminal(uint16, uint16) (*os.File, *os.File, error) {
	return nil, nil, fmt.Errorf("PTY requires Linux")
}
func resizeTerminal(*os.File, uint16, uint16) error { return fmt.Errorf("PTY requires Linux") }
func configureTerminal(*exec.Cmd)                   {}
