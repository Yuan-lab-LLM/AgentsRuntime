//go:build linux

package slots

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSourceStoppedWriterProofRejectsForeignOwnersAndFIFO(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("foreign ownership checks require disposable root test environment")
	}
	for _, name := range []string{"root", "directory", "lock", "record", "record FIFO", "lock FIFO"} {
		t.Run(name, func(t *testing.T) {
			p, req, lock, record := sourceProofFixture(t)
			path := record
			switch name {
			case "root":
				path = p.cfg.WorkspaceRoot
			case "directory":
				path = filepath.Dir(record)
			case "lock", "lock FIFO":
				path = lock
			}
			if name == "record FIFO" || name == "lock FIFO" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chown(path, 65534, 65534); err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(req)
			out := sourceProofCall(p, body, "control", "POST", true)
			if out.Code != 409 {
				t.Fatalf("foreign owner or FIFO accepted: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
