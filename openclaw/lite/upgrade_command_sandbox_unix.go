//go:build linux || darwin

package openclaw

import (
	"os/exec"
	"syscall"
)

func configureUpgradeSandboxProcess(cmd *exec.Cmd, uid, gid int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
}
