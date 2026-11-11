package gateway

// ExecutionIdle is positive supervisor evidence, not a scan for a missing PID.
// A finished record retains its teardown debt until every owned resource joined.
func (m *GatewayManager) ExecutionIdle() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.gateways {
		if r.reserved || r.process.PID != 0 || r.executionCleanup != nil || r.automationStopFailed || r.activeCommands != 0 || !recordFinished(r) {
			return false
		}
		if r.workbench != nil && r.workbench.ctx.Err() == nil {
			return false
		}
	}
	return true
}
