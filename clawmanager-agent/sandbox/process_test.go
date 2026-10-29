package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func procStat(pid, ppid int, state, start string) string {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[1], fields[19] = state, strconv.Itoa(ppid), start
	return strconv.Itoa(pid) + " (a command ) with spaces) " + strings.Join(fields, " ") + "\n"
}

func TestParseProcessIdentityUsesStartTimeAfterCommandName(t *testing.T) {
	identity, state, ppid, err := parseProcessIdentity(123, []byte(procStat(123, 42, "S", "4567")))
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != 123 || identity.StartTime != 4567 || state != "S" || ppid != 42 {
		t.Fatalf("incorrect identity: %+v %s %d", identity, state, ppid)
	}
	for _, malformed := range []string{"", "123 bad", "123 (command) S 1", procStat(123, 42, "S", "bad")} {
		if _, _, _, err := parseProcessIdentity(123, []byte(malformed)); err == nil {
			t.Fatalf("accepted malformed stat %q", malformed)
		}
	}
}

func TestTrackerAddsOnlyRecognizedLifecycleFDs(t *testing.T) {
	cmd := exec.Command("/usr/bin/bwrap", "--unshare-pid", "--", "/bin/sh")
	tracker, err := newTracker(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if !containsArgs(cmd.Args, []string{"--info-fd", "3", "--block-fd", "4", "--seccomp", "5"}) || len(cmd.ExtraFiles) != 3 {
		t.Fatalf("unexpected tracking command: %v, FDs=%d", cmd.Args, len(cmd.ExtraFiles))
	}
	cmd = exec.Command("/usr/bin/bwrap", "--unshare-pid", "--", "/bin/sh")
	cmd.ExtraFiles = []*os.File{os.Stdin}
	if _, err := newTracker(cmd); err == nil {
		t.Fatal("accepted unrelated inherited fd")
	}
}

func TestTrackerWaitsForNamespaceInitIdentityRatherThanOuterExit(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "123"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "123", "stat")
	for _, tc := range []struct {
		name, state, start string
		stopped            bool
	}{
		{"still running", "S", "4567", false}, {"dead PID1", "Z", "4567", true}, {"PID reused", "S", "9999", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(procStat(123, 42, tc.state, tc.start)), 0600); err != nil {
				t.Fatal(err)
			}
			tracker := &Tracker{identity: ProcessIdentity{PID: 123, StartTime: 4567}, captured: true, procRoot: root}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			err := tracker.WaitStopped(ctx)
			if tc.stopped && err != nil {
				t.Fatal(err)
			}
			if !tc.stopped && err == nil {
				t.Fatal("reported stopped while namespace init still running")
			}
		})
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	tracker := &Tracker{identity: ProcessIdentity{PID: 123, StartTime: 4567}, captured: true, procRoot: root}
	if err := tracker.WaitStopped(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (&Tracker{}).WaitStopped(context.Background()); err == nil {
		t.Fatal("accepted missing namespace identity")
	}
}

func TestTrackerCaptureRejectsUnrelatedProcessOrNonInit(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "123"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "stat"), []byte(procStat(123, 42, "S", "4567")), 0600); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(root, "123", "status")
	tracker := &Tracker{procRoot: root}
	for _, ns := range []string{"NSpid:\t123\t2\n", "Name:\tbwrap\n"} {
		if err := os.WriteFile(status, []byte(ns), 0600); err != nil {
			t.Fatal(err)
		}
		if err := tracker.capture(123, 42); err == nil {
			t.Fatal("accepted non-init process")
		}
	}
	if err := os.WriteFile(status, []byte("NSpid:\t123\t1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tracker.capture(123, 999); err == nil {
		t.Fatal("accepted unrelated parent")
	}
	if err := tracker.capture(123, 42); err != nil {
		t.Fatal(err)
	}
	if tracker.identity.StartTime != 4567 || !tracker.captured {
		t.Fatal("missing captured identity")
	}
}

func TestTrackerStartedCapturesIdentityBeforeReleasingGateway(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "123"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "stat"), []byte(procStat(123, 99999999, "S", "4567")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "status"), []byte("NSpid:\t123\t1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/usr/bin/bwrap", "--unshare-pid", "--", "/bin/sh")
	tracker, err := newTracker(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	tracker.procRoot = root
	process, err := os.FindProcess(99999999)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	cmd.Process = process
	if _, err := tracker.infoW.Write([]byte(`{"child-pid":123}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Started(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tracker.captured || !tracker.released || tracker.identity.PID != 123 {
		t.Fatal("gateway released without captured identity")
	}
	if err := tracker.Started(context.Background()); err == nil {
		t.Fatal("accepted duplicate startup")
	}
}

func TestPrimaryGatewayMustBeNamespacePID2UnderCapturedInit(t *testing.T) {
	root := t.TempDir()
	for _, pid := range []string{"123", "456", "777", "999"} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "123", "task", "123"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		pid, ppid int
		ns, start string
	}{
		{123, 42, "1", "4567"}, {999, 321, "2", "9000"}, {777, 123, "3", "8000"}, {456, 123, "2", "7000"},
	} {
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(entry.pid), "stat"), []byte(procStat(entry.pid, entry.ppid, "S", entry.start)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(entry.pid), "status"), []byte("NSpid:\t"+strconv.Itoa(entry.pid)+"\t"+entry.ns+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	children := filepath.Join(root, "123", "task", "123", "children")
	if err := os.WriteFile(children, []byte("999 777 456"), 0600); err != nil {
		t.Fatal(err)
	}
	tracker := &Tracker{procRoot: root, captured: true, identity: ProcessIdentity{PID: 123, StartTime: 4567}}
	identity, err := tracker.primaryGateway()
	if err != nil || identity.PID != 456 || identity.StartTime != 7000 {
		t.Fatalf("wrong primary gateway %+v: %v", identity, err)
	}
	if err := os.WriteFile(children, []byte("999 777"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.primaryGateway(); !errors.Is(err, errGatewayPending) {
		t.Fatalf("expected gateway pending, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "123", "stat"), []byte(procStat(123, 42, "S", "9999")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.primaryGateway(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("PID1 reuse accepted: %v", err)
	}
}

func TestNamespaceInitDisappearanceAcceptsOnlyMissingProcessErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		gone bool
	}{
		{"missing entry", syscall.ENOENT, true},
		{"read missing entry", &os.PathError{Op: "read", Path: "/proc/44/stat", Err: syscall.ENOENT}, true},
		{"task exited", syscall.ESRCH, true},
		{"proc read exit race", &os.PathError{Op: "read", Path: "/proc/44/stat", Err: syscall.ESRCH}, true},
		{"permission denied", syscall.EPERM, false},
		{"read permission denied", &os.PathError{Op: "read", Path: "/proc/44/stat", Err: syscall.EPERM}, false},
		{"access denied", &os.PathError{Op: "open", Path: "/proc/44/stat", Err: syscall.EACCES}, false},
		{"IO failure", &os.PathError{Op: "read", Path: "/proc/44/stat", Err: syscall.EIO}, false},
		{"invalid argument", syscall.EINVAL, false},
		{"unknown error text", errors.New("no such process"), false},
		{"mixed uncertain errors", errors.Join(syscall.ESRCH, syscall.EPERM), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceInitDisappeared(tc.err); got != tc.gone {
				t.Fatalf("namespace disappearance=%v want %v for %T: %v", got, tc.gone, tc.err, tc.err)
			}
		})
	}
}
