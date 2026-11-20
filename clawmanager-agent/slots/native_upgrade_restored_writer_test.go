package slots

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestNativeRecoveryRetainsRealStoppedWriterAcrossUnpublishedGenerations(t *testing.T) {
	for _, name := range []string{"same", "retry", "retry-resume", "rebuild", "ordinary-gap", "unknown-mode", "live-older", "upgrading-older", "newer", "wrong-owner", "wrong-runtime", "missing-generation"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".clawmanager-slot-locks")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "openclaw-1-7")
			if err := os.WriteFile(base+".lock", nil, 0600); err != nil {
				t.Fatal(err)
			}
			before := writerRecord{InstanceID: 7, UserID: 1, Generation: 2, RuntimeType: "openclaw", PodUID: "real-source", Slot: "slot-0", State: "stopped"}
			req := gateway.WorkspaceUpgradeRequest{InstanceID: 7, UserID: 1, Generation: 4, RecoveryMode: "retry"}
			switch name {
			case "same":
				req.Generation = 2
				req.RecoveryMode = ""
			case "rebuild":
				req.RecoveryMode = "rebuild"
			case "ordinary-gap":
				req.RecoveryMode = ""
			case "unknown-mode":
				req.RecoveryMode = "reset"
			case "live-older":
				before.State = "running"
			case "upgrading-older":
				before.State = "upgrading"
			case "newer":
				before.Generation = 5
			case "wrong-owner":
				before.UserID = 9
			case "wrong-runtime":
				before.RuntimeType = "hermes"
			case "missing-generation":
				before.Generation = 0
			}
			if err := atomicJSON(base+".json", before); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(base + ".json")
			if err != nil {
				t.Fatal(err)
			}
			s := &nativeUpgradeSession{record: &nativeUpgradeRecord{Operation: gateway.NativeUpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", Request: req}, PodUID: "target"}, path: filepath.Join(root, "maintenance.json")}
			err = s.acquireWriter(gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", PodUID: "target"})
			allowed := name == "same" || name == "retry" || name == "retry-resume" || name == "rebuild"
			if (err == nil) != allowed {
				t.Fatalf("writer admission: %v", err)
			}
			if allowed {
				if name == "retry-resume" {
					s.writer.Close()
					resumed := &nativeUpgradeSession{record: s.record, path: s.path}
					if err := resumed.acquireWriter(gateway.Config{WorkspaceRoot: root, RuntimeType: "openclaw", PodUID: "target"}); err != nil {
						t.Fatal("lost retained recovery after restart", err)
					}
					s = resumed
				}
				if s.record.Previous == nil || *s.record.Previous != before {
					t.Fatal("original writer identity was invented")
				}
				if err := s.restoreWriter(); err != nil {
					t.Fatal(err)
				}
				s.writer.Close()
			}
			after, err := os.ReadFile(base + ".json")
			if err != nil || !bytes.Equal(after, original) {
				t.Fatalf("writer provenance changed: %v", err)
			}
		})
	}
}
