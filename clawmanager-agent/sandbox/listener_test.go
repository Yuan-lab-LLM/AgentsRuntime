package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func listenerFixture(t *testing.T) (*Tracker, string, string) {
	t.Helper()
	root := t.TempDir()
	own, other := filepath.Join(root, "own-namespace"), filepath.Join(root, "other-namespace")
	for _, path := range []string{own, other} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct {
		pid              int
		namespace, inode string
	}{{123, own, ""}, {456, own, "111"}, {789, other, "222"}} {
		proc := filepath.Join(root, strconv.Itoa(entry.pid))
		for _, path := range []string{filepath.Join(proc, "ns"), filepath.Join(proc, "fd")} {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(proc, "stat"), []byte(procStat(entry.pid, 123, "S", strconv.Itoa(entry.pid*100))), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(entry.namespace, filepath.Join(proc, "ns", "pid")); err != nil {
			t.Fatal(err)
		}
		if entry.inode != "" {
			if err := os.Symlink("socket:["+entry.inode+"]", filepath.Join(proc, "fd", "3")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
0: 0100007F:4E20 00000000:0000 0A 00000000:00000000 00:00000000 00000000 200001 0 111
1: 0100007F:4E21 00000000:0000 0A 00000000:00000000 00:00000000 00000000 200002 0 222
2: 0100007F:4E22 00000000:0000 01 00000000:00000000 00:00000000 00000000 200001 0 333
3: 0100007F:7530 00000000:0000 0A 00000000:00000000 00:00000000 00000000 200002 0 444
`
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(table), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(own, filepath.Join(root, "123", "ns", "net")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "123", "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "net", "tcp"), []byte(table), 0600); err != nil {
		t.Fatal(err)
	}
	return &Tracker{procRoot: root, captured: true, identity: ProcessIdentity{PID: 123, StartTime: 12300}}, own, other
}

func TestListenersMustBelongToCapturedNamespaceAcrossAssignedBlock(t *testing.T) {
	tracker, own, _ := listenerFixture(t)
	defer tracker.Close()
	if err := tracker.VerifyListeners(context.Background(), 20000, 1); err != nil {
		t.Fatalf("owned primary listener rejected: %v", err)
	}
	if err := tracker.VerifyListeners(context.Background(), 20000, 3); err == nil {
		t.Fatal("accepted another namespace's optional business listener")
	}
	path := filepath.Join(tracker.procRoot, "789", "ns", "pid")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(own, path); err != nil {
		t.Fatal(err)
	}
	if err := tracker.VerifyListeners(context.Background(), 20000, 3); err != nil {
		t.Fatalf("owned listeners rejected: %v", err)
	}
}

func TestListenersRejectMissingPrimaryOrChangedInitIdentity(t *testing.T) {
	tracker, _, _ := listenerFixture(t)
	defer tracker.Close()
	if err := tracker.VerifyListeners(context.Background(), 20002, 1); err == nil {
		t.Fatal("nonlistening primary accepted")
	}
	if err := os.WriteFile(filepath.Join(tracker.procRoot, "123", "stat"), []byte(procStat(123, 42, "S", "99999")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tracker.VerifyListeners(context.Background(), 20000, 1); err == nil {
		t.Fatal("namespace init PID reuse accepted")
	}
	if err := (&Tracker{}).VerifyListeners(context.Background(), 20000, 1); err == nil {
		t.Fatal("missing namespace identity accepted")
	}
}

func TestCollectBusinessListenersRejectsMalformedTable(t *testing.T) {
	for _, row := range []string{"0: malformed 00000000:0000 0A 0 0 0 0 0 123", "0: 0100007F:ZZZZ 00000000:0000 0A 0 0 0 0 0 123"} {
		if _, _, err := collectBusinessListeners(strings.NewReader(row), 20000, 1); err == nil {
			t.Fatalf("malformed listening socket accepted: %q", row)
		}
	}
}

func TestListenerInspectionUsesPinnedBusinessNetwork(t *testing.T) {
	tracker, _, other := listenerFixture(t)
	defer tracker.Close()
	proc := filepath.Join(tracker.procRoot, "123")
	table, err := os.ReadFile(filepath.Join(tracker.procRoot, "net", "tcp"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(proc, "net", "tcp"), table, 0600); err != nil {
		t.Fatal(err)
	}
	// A trusted ingress bridge may listen in the outer namespace. It must not
	// be mistaken for a business listener or cause a false ownership failure.
	if err = os.WriteFile(filepath.Join(tracker.procRoot, "net", "tcp"), []byte("0: 0100007F:4E20 00000000:0000 0A 0 0 0 0 0 999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = tracker.VerifyListeners(context.Background(), 20000, 1); err != nil {
		t.Fatalf("read outer network table: %v", err)
	}
	if err = os.Remove(filepath.Join(proc, "ns", "net")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(other, filepath.Join(proc, "ns", "net")); err != nil {
		t.Fatal(err)
	}
	if err = tracker.VerifyListeners(context.Background(), 20000, 1); err == nil {
		t.Fatal("accepted replaced network namespace")
	}
}
