package gateway

import (
	"encoding/json"
	"testing"
)

func TestHeartbeatPreservesConfiguredCapacityAndLegacyMaxGateways(t *testing.T) {
	m := NewGatewayManager(Config{Capacity: 3, GatewayPortStart: 20300, GatewayPortEnd: 20399, GatewayPortBlockSize: 1}, nil, nil)
	heartbeat := m.HeartbeatPayload(1)
	registered := m.RegisterPayload()
	raw, err := json.Marshal(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if heartbeat.Capacity != 3 || registered.Capacity != 3 || fields["capacity"] != float64(3) || fields["max_gateways"] != float64(3) {
		t.Fatalf("capacity changed after registration: %s", raw)
	}
}
