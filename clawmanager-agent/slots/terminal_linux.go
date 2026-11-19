//go:build linux

package slots

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func terminalIOCTL(file *os.File, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}
func openTerminal(cols, rows uint16) (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			master.Close()
			if slave != nil {
				slave.Close()
			}
		}
	}()
	unlock := int32(0)
	if err = terminalIOCTL(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return
	}
	var number uint32
	if err = terminalIOCTL(master, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		return
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return
	}
	err = resizeTerminal(master, cols, rows)
	return
}
func resizeTerminal(master *os.File, cols, rows uint16) error {
	size := [4]uint16{rows, cols, 0, 0}
	return terminalIOCTL(master, syscall.TIOCSWINSZ, unsafe.Pointer(&size))
}
func configureTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
}
