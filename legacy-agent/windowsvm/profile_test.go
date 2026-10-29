package windowsvm

import (
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestProfileDefaults(t *testing.T) {
	profile := NewProfile("windows-vm")
	defaults := profile.Defaults()

	if profile.Type() != "windows-vm" {
		t.Fatalf("Type() = %q, want windows-vm", profile.Type())
	}
	if profile.DisplayName() != "Windows VM" {
		t.Fatalf("DisplayName() = %q, want Windows VM", profile.DisplayName())
	}
	if defaults.GatewayCapacity != 1 {
		t.Fatalf("GatewayCapacity = %d, want 1", defaults.GatewayCapacity)
	}
	if defaults.GatewayPortStart != 8006 || defaults.GatewayPortEnd != 8006 {
		t.Fatalf("gateway port range = %d-%d, want 8006-8006", defaults.GatewayPortStart, defaults.GatewayPortEnd)
	}
}

func TestGatewayEnvAddsWindowsPaths(t *testing.T) {
	profile := NewProfile("windows-vm")
	env := profile.GatewayEnv(nil, gateway.Config{RuntimeType: "windows-vm"}, gateway.CreateGatewayRequest{
		InstanceID: 7,
		UserID:     9,
	}, "/workspaces/windows-vm/user-9/instance-7", 8006)

	got := envMap(env)
	if got["WINDOWS_VM_STORAGE_DIR"] != "/workspaces/windows-vm/user-9/instance-7/storage" {
		t.Fatalf("WINDOWS_VM_STORAGE_DIR = %q", got["WINDOWS_VM_STORAGE_DIR"])
	}
	if got["WINDOWS_VM_SHARED_DIR"] != "/workspaces/windows-vm/user-9/instance-7/shared" {
		t.Fatalf("WINDOWS_VM_SHARED_DIR = %q", got["WINDOWS_VM_SHARED_DIR"])
	}
	if got["CLAWMANAGER_GATEWAY_PORT"] != "8006" {
		t.Fatalf("CLAWMANAGER_GATEWAY_PORT = %q", got["CLAWMANAGER_GATEWAY_PORT"])
	}
}

func envMap(values []string) map[string]string {
	out := map[string]string{}
	for _, value := range values {
		for index, r := range value {
			if r == '=' {
				out[value[:index]] = value[index+1:]
				break
			}
		}
	}
	return out
}
