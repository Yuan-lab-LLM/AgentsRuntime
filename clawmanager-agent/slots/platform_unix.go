//go:build linux || darwin

package slots

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func sandboxDirectoryGroup(path string) (uint32, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("Team source is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("Team source group is unavailable")
	}
	return stat.Gid, nil
}

func configureSandboxProcess(cmd *exec.Cmd, uid, gid int, groups []uint32) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
	return nil
}
func lockWorkspace(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
