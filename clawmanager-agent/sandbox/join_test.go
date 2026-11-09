package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJoinedCommandReusesPIDWithoutExpandingFilesystemGrants(t *testing.T) {
	spec := testSpec(t)
	spec.joinExecution = true
	args, err := buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " --userns 6 --userns2 7 --pidns 8 ") || strings.Contains(joined, " --unshare-pid ") {
		t.Fatal("terminal does not join the primary execution")
	}
	if !strings.Contains(joined, " --unshare-ipc ") || !strings.Contains(joined, " --unshare-uts ") {
		t.Fatal("non-networked legacy terminal lost IPC/UTS isolation")
	}
	if !strings.Contains(joined, " --proc /proc ") || !strings.Contains(joined, " --cap-drop ALL ") {
		t.Fatal("terminal lost process view or privilege confinement")
	}
}

func TestJoinedTrackerAcceptsOnlyTrustedIntermediate(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"123", "77"} {
		if err := os.Mkdir(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "123/stat"), []byte(procStat(123, 77, "S", "4567")), 0600)
	os.WriteFile(filepath.Join(root, "123/status"), []byte("Uid:\t200007\t200007\t200007\t200007\nNSpid:\t123\t3\n"), 0600)
	for _, test := range []struct {
		name, state, start string
		parent             int
		joined, valid      bool
	}{
		{"live exact monitor", "S", "4566", 42, true, true},
		{"zombie exact monitor", "Z", "4566", 42, true, true},
		{"live unrelated monitor", "S", "4566", 99, true, false},
		{"zombie unrelated monitor", "Z", "4566", 99, true, false},
		{"external Pod init", "Z", "4566", 1, true, false},
		{"dead intermediate", "X", "4566", 42, true, false},
		{"later intermediate identity", "Z", "4568", 42, true, false},
		{"nonjoined live fork", "S", "4566", 42, false, false},
		{"nonjoined zombie fork", "Z", "4566", 42, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			os.WriteFile(filepath.Join(root, "77/stat"), []byte(procStat(77, test.parent, test.state, test.start)), 0600)
			tracker := &Tracker{containerLifecycle: true, auxiliaryMountScope: test.joined, businessUID: 200007, procRoot: root}
			if err := tracker.capture(123, 42); (err == nil) != test.valid {
				t.Fatalf("parent=%d state=%s joined=%v accepted=%v: %v", test.parent, test.state, test.joined, err == nil, err)
			}
		})
	}
}
