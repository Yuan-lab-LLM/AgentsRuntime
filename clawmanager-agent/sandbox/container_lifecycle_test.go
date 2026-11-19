package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerLifecycleReusesMaskedReadonlyProc(t *testing.T) {
	spec := testSpec(t)
	spec.ContainerLifecycle = true
	argv, err := buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(argv, " ") + " "
	for _, denied := range []string{" --unshare-pid ", " --disable-userns ", " --proc "} {
		if strings.Contains(joined, denied) {
			t.Fatalf("container sandbox requests unsupported setup: %s", denied)
		}
	}
	for _, required := range []string{" --unshare-user ", " --ro-bind /proc /proc ", " --cap-drop ALL "} {
		if !strings.Contains(joined, required) {
			t.Fatalf("container sandbox lacks mandatory isolation: %s", required)
		}
	}
}

func TestContainerTrackerCapturesOnlyFixedOuterBusinessUID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "123")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(procStat(123, 42, "S", "4567")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		uid   string
		valid bool
	}{{"200007\t200007\t200007\t200007", true}, {"0\t0\t0\t0", false}, {"200007\t10001\t200007\t200007", false}, {"", false}} {
		status := "NSpid:\t123\n"
		if test.uid != "" {
			status += "Uid:\t" + test.uid + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0600); err != nil {
			t.Fatal(err)
		}
		tracker := &Tracker{containerLifecycle: true, procRoot: root}
		err := tracker.capture(123, 42)
		if (err == nil) != test.valid {
			t.Fatalf("UID %q accepted=%v", test.uid, err == nil)
		}
		if test.valid && tracker.businessUID != 200007 {
			t.Fatal("business UID not pinned")
		}
	}
}

func TestContainerTrackerNeverConfirmsExecutionFromPrimaryExit(t *testing.T) {
	cmd := exec.Command("/usr/bin/bwrap", "--unshare-user", "--", "/bin/true")
	tracker, err := TrackContainer(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if err := tracker.WaitStopped(context.Background()); !errors.Is(err, ErrContainerResetRequired) {
		t.Fatalf("shared-PID tracker claimed whole execution stopped: %v", err)
	}
}
