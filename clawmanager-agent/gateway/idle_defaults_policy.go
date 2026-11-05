package gateway

// Defaults policy is display metadata, not an additional stop eligibility gate.
type IdleDefaultsPolicy struct {
	DefaultsPolicyVersion string `json:"defaults_policy_version,omitempty"`
	DefaultsPolicyStatus  string `json:"defaults_policy_status"`
}
type ManagedGatewayIdleDefaultsPolicyProvider interface {
	GatewayIdleDefaultsPolicy(Config, CreateGatewayRequest, GatewayStartSpec) IdleDefaultsPolicy
}

func (m *GatewayManager) idleDefaultsPolicy(req CreateGatewayRequest, spec GatewayStartSpec) IdleDefaultsPolicy {
	if provider, ok := runtimeProfile(m.cfg).(ManagedGatewayIdleDefaultsPolicyProvider); ok {
		return provider.GatewayIdleDefaultsPolicy(m.cfg, req, spec)
	}
	return IdleDefaultsPolicy{DefaultsPolicyStatus: "unknown"}
}
