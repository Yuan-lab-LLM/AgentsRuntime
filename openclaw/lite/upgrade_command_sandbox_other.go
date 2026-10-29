//go:build !linux && !darwin

package openclaw

import "os/exec"

// sandbox.Command rejects unsupported hosts before process construction.
func configureUpgradeSandboxProcess(*exec.Cmd, int, int) {}
