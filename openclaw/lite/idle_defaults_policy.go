package openclaw

import (
	"encoding/json"
	"path/filepath"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const idleDefaultsPolicyVersion = "lite-idle-off-v1"

type idleDefaultsPolicy struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

func (p Profile) GatewayIdleDefaultsPolicy(cfg gateway.Config, req gateway.CreateGatewayRequest, spec gateway.GatewayStartSpec) gateway.IdleDefaultsPolicy {
	result := gateway.IdleDefaultsPolicy{DefaultsPolicyStatus: "unknown"}
	// Read the bounded, per-instance policy marker. It records configuration
	// provenance only, never proof of a running native scheduler's behavior.
	s, err := openOpenClawIO(spec.WorkspacePath, cfg.WorkspaceRoot)
	if err != nil {
		return result
	}
	defer s.Close()
	raw, err := s.ReadFile(filepath.Join(spec.WorkspacePath, ".clawmanager-idle-defaults-policy.json"))
	if err != nil {
		return result
	}
	var policy idleDefaultsPolicy
	if json.Unmarshal(raw, &policy) != nil || policy.Version != idleDefaultsPolicyVersion {
		return result
	}
	result.DefaultsPolicyVersion = policy.Version
	result.DefaultsPolicyStatus = policy.Status
	return result
}
