//go:build linux

package slots

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

func serveOfflineMaintenanceProbe() {
	result := slotNetworkProbeResult{Checks: map[string]bool{}}
	result.Namespace, _ = os.Readlink("/proc/self/ns/net")
	interfaces, err := net.Interfaces()
	result.Checks["disconnected_net"] = err == nil && len(interfaces) == 1 && interfaces[0].Name == "lo"
	for _, target := range []string{"127.0.0.1:28444", "192.168.9.9:443", "8.8.8.8:443"} {
		conn, err := net.DialTimeout("tcp", target, time.Second)
		result.Checks[target] = err != nil
		if conn != nil {
			conn.Close()
		}
	}
	conn, err := net.DialTimeout("unix", "@cm-offline-maintenance-private", time.Second)
	result.Checks["abstract_denied"] = err != nil
	if conn != nil {
		conn.Close()
	}
	result.Checks["no_capability_env"] = true
	for _, item := range os.Environ() {
		if strings.Contains(item, slotProbeControl) || strings.Contains(item, "v1.probe-") {
			result.Checks["no_capability_env"] = false
		}
	}
	_, err = os.ReadFile("/tmp/cm-offline-sentinel")
	result.Checks["fresh_tmp"] = os.IsNotExist(err)
	err = os.WriteFile("/tmp/cm-offline-sentinel", []byte("local-doctor-output"), 0600)
	data, readErr := os.ReadFile("/tmp/cm-offline-sentinel")
	result.Checks["local_file_operation"] = err == nil && readErr == nil && string(data) == "local-doctor-output"
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func TestProtectedOfflineMaintenanceLinuxAcceptance(t *testing.T) {
	if os.Getenv("TEST_PROTECTED_SLOT_DISPOSABLE") != "1" {
		t.Skip("requires disposable Linux namespace acceptance Pod")
	}
	if os.Geteuid() != 0 {
		t.Fatal("trusted userns root required")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := "/workspaces/cm-offline-maintenance-acceptance"
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(base, "openclaw", "user-1", "instance-107")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	outer, err := net.Listen("tcp", "127.0.0.1:28444")
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	abstract, err := net.Listen("unix", "@cm-offline-maintenance-private")
	if err != nil {
		t.Fatal(err)
	}
	defer abstract.Close()
	direct := slotProbePrivateControl(t)
	outerNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		target := "/workspaces/openclaw/user-1/instance-107"
		argv, err := sandbox.Command(sandbox.Spec{OfflineNetwork: true, Args: []string{executable, slotProbeOffline}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: target, UID: 200107, GID: 200107, Mounts: []sandbox.Mount{{Source: filepath.Clean(workspace), Target: target, ReadOnly: true}}})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		gateway.ConfigureGatewayCommand(cmd, 200107, 200107)
		data, err := cmd.CombinedOutput()
		var result slotNetworkProbeResult
		if err != nil || json.Unmarshal(data, &result) != nil {
			t.Fatalf("offline command: %v %s", err, data)
		}
		if result.Namespace == "" || result.Namespace == outerNamespace {
			t.Fatal("maintenance inherited management NET")
		}
		for name, passed := range result.Checks {
			if !passed {
				t.Errorf("offline attempt %d failed %s", attempt, name)
			}
		}
		if len(result.Checks) != 8 || direct.Load() != 0 {
			t.Fatal("offline evidence missing or private service reached")
		}
	}
	t.Log("two actual non-root offline commands: disconnected fresh NET/tmp, local file operations, outer localhost/private live controls denied")
}
