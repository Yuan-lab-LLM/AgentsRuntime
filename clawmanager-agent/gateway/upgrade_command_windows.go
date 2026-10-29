//go:build windows

package gateway

import "os/exec"

// Windows is used only by unit tests, never advertised as a fenced Runtime.
func fenceUpgradeCommand(cmd *exec.Cmd) func() { return func() {} }
