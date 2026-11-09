package gateway

import (
	"fmt"
	"strconv"
)

// RunListenerProbe handles only the private, read-only listener inspection
// subcommand. It must run before loading runtime configuration so its minimal
// environment needs neither control-plane credentials nor instance secrets.
func RunListenerProbe(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--verify-hermes-listener" {
		return false, nil
	}
	invalid := func() (bool, error) {
		return true, fmt.Errorf("port_conflict: invalid managed listener inspection request")
	}
	if len(args) != 3 {
		return invalid()
	}
	pid, err := strconv.Atoi(args[1])
	if err != nil || pid <= 1 || strconv.Itoa(pid) != args[1] {
		return invalid()
	}
	port, err := strconv.Atoi(args[2])
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != args[2] {
		return invalid()
	}
	return true, validateIsolatedListener(pid, port)
}
