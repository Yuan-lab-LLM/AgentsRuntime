//go:build !linux

package gateway

import "fmt"

func (s *ExecProcessStarter) VerifyListener(spec GatewayStartSpec, pid int) error {
	return validateIsolatedListener(pid, spec.Port)
}

func validateIsolatedListener(pid, port int) error {
	return fmt.Errorf("unsupported_hermes_protocol: managed listener verification requires Linux")
}
