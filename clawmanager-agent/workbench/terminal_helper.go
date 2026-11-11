package workbench

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"syscall"
)

const TerminalCommand = "managed-workbench-terminal"
const SharedTerminalCommand = "managed-workbench-instance-terminal"

// The readiness line is emitted only after entering the final business sandbox.
// Parent pins that namespace before releasing the interactive shell.
func RunTerminalHelper(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || (args[0] != TerminalCommand && args[0] != SharedTerminalCommand) {
		return false, nil
	}
	if len(args) != 2 || len(args[1]) != 32 {
		return true, Invalid()
	}
	for _, c := range args[1] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return true, Invalid()
		}
	}
	if args[0] == SharedTerminalCommand {
		if err := prepareSharedTerminal(input); err != nil {
			return true, err
		}
	}
	if _, err := fmt.Fprintln(output, args[1]); err != nil {
		return true, err
	}
	line, err := bufio.NewReader(io.LimitReader(input, 2)).ReadString('\n')
	if err != nil || line != "\n" {
		return true, Invalid()
	}
	return true, syscall.Exec("/bin/sh", []string{"/bin/sh", "-i"}, os.Environ())
}
