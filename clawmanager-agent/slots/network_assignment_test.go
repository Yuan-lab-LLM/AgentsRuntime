package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"strings"
	"testing"
)

func TestNetworkAssignmentUsesActualSlotCapacityAndFixedPortBlock(t *testing.T) {
	for _, count := range []int{1, 4, 8, 16, 256} {
		cfg := gateway.Config{NetworkSecurityProfile: "isolated-network-v1", NetworkSlotCount: count, NetworkSlotIndex: count - 1, ControlToken: strings.Repeat("c", 32), PodUID: "pod-uid", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
		req := gateway.CreateGatewayRequest{InstanceID: 1, UserID: 2, Generation: 3, PodUID: cfg.PodUID, SecurityProfile: cfg.NetworkSecurityProfile, UID: 200001, GID: 200001, Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_" + strings.Repeat("n", 32)}}
		if _, err := validateNetworkAssignment(cfg, req, 20000); err != nil {
			t.Fatalf("count %d: %v", count, err)
		}
		req.PortRange = gateway.PortRange{Start: 20000, End: 20003}
		if _, err := validateNetworkAssignment(cfg, req, 20000); err == nil {
			t.Fatal("accepted an ingress port outside fixed block")
		}
		req.PortRange = gateway.PortRange{}
		cfg.NetworkSlotIndex = count
		if _, err := validateNetworkAssignment(cfg, req, 20000); err == nil {
			t.Fatalf("count %d accepted out-of-range slot", count)
		}
		cfg.NetworkSlotIndex = 0
		if _, err := validateNetworkAssignment(cfg, req, 20001); err == nil {
			t.Fatal("accepted caller-selected port")
		}
	}
}
