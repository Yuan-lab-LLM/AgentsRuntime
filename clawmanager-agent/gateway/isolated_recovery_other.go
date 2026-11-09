//go:build !linux

package gateway

import "fmt"

func recordIsolatedProcess(Config, GatewayStartSpec, int) (func(), error) {
	return nil, fmt.Errorf("unsupported_hermes_protocol: managed process recovery requires Linux")
}

func recoverIsolatedProcesses(Config) error {
	return fmt.Errorf("unsupported_hermes_protocol: managed process recovery requires Linux")
}
