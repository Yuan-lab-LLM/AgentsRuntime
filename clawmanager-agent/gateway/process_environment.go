package gateway

import "os"

func (m *GatewayManager) gatewayBaseEnvironment() []string {
	if m.cfg.GatewayBaseEnv != nil {
		return append([]string{}, m.cfg.GatewayBaseEnv...)
	}
	return os.Environ()
}
