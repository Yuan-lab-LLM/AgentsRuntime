//go:build linux || darwin

package sandbox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestJoinedTrackerRechecksOnlyExactMonitorAfterReparent(t *testing.T) {
	for _, test := range []struct {
		name, state, start string
		parent             int
		valid              bool
	}{
		{"same child adopted by monitor", "S", "4567", 42, true},
		{"foreign parent", "S", "4567", 99, false},
		{"external Pod init", "S", "4567", 1, false},
		{"reused child PID", "S", "9999", 42, false},
		{"exited child", "Z", "4567", 42, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, pid := range []string{"123", "77"} {
				if err := os.Mkdir(filepath.Join(root, pid), 0700); err != nil {
					t.Fatal(err)
				}
			}
			child := filepath.Join(root, "123/stat")
			parent := filepath.Join(root, "77/stat")
			if err := os.WriteFile(child, []byte(procStat(123, 77, "S", "4567")), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "123/status"), []byte("Uid:\t200007\t200007\t200007\t200007\nNSpid:\t123\t3\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(parent, 0600); err != nil {
				t.Fatal(err)
			}
			changed := make(chan error, 1)
			go func() {
				// FIFO rendezvous proves capture read the old child PPid before reparent.
				file, err := os.OpenFile(parent, os.O_WRONLY, 0)
				if err != nil {
					changed <- err
					return
				}
				defer file.Close()
				err = os.WriteFile(child, []byte(procStat(123, test.parent, test.state, test.start)), 0600)
				if err == nil {
					_, err = file.Write([]byte("intermediate was reaped"))
				}
				changed <- err
			}()
			tracker := &Tracker{containerLifecycle: true, auxiliaryMountScope: true, businessUID: 200007, procRoot: root}
			err := tracker.capture(123, 42)
			if changeErr := <-changed; changeErr != nil {
				t.Fatal(changeErr)
			}
			if (err == nil) != test.valid {
				t.Fatalf("accepted=%v want %v: %v", err == nil, test.valid, err)
			}
		})
	}
}
