package slots

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestMaintenanceWriterRetainsQuarantineWithoutNamespaceProof(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "uncertain"}[confirmed], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", PodUID: "maintenance-pod"}
			dir := filepath.Join(root, ".clawmanager-slot-locks")
			if err = os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "openclaw-2-7")
			if err = os.WriteFile(base+".lock", nil, 0600); err != nil {
				t.Fatal(err)
			}
			previous := writerRecord{InstanceID: 7, UserID: 2, Generation: 5, RuntimeType: "openclaw", PodUID: "source", Slot: "slot-0", State: "stopped"}
			if err = atomicJSON(base+".json", previous); err != nil {
				t.Fatal(err)
			}
			req := gateway.WorkspaceUpgradeRequest{InstanceID: 7, UserID: 2, Generation: 5}
			finish, err := AcquireStoppedCommandWriter(cfg, req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = AcquireStoppedCommandWriter(cfg, req); err == nil {
				t.Fatal("maintenance writer ran concurrently")
			}
			if err = finish(confirmed); err != nil {
				t.Fatal(err)
			}
			next := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 6, AgentType: "openclaw", PodUID: "target"}
			err = claimWriter(base+".json", next, "slot-1")
			if confirmed && err != nil {
				t.Fatal(err)
			}
			if !confirmed && err == nil {
				t.Fatal("uncertain maintenance teardown authorized a gateway writer")
			}
		})
	}
}
