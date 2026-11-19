package gateway

import "testing"

func TestNativeStorageConfigPreservesAssignedPortBounds(t *testing.T) {
	cfg := Config{GatewayPortStart: 23000, GatewayPortEnd: 23000, GatewayPortBlockSize: 1}
	got := NativeConfig(cfg).config(nil)
	if got.GatewayPortStart != cfg.GatewayPortStart || got.GatewayPortEnd != cfg.GatewayPortEnd || got.GatewayPortBlockSize != cfg.GatewayPortBlockSize {
		t.Fatal("native validation lost authoritative assigned port bounds")
	}
}
