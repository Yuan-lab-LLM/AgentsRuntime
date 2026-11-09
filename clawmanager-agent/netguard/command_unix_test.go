//go:build linux || darwin

package netguard

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func trackedCommand(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/usr/bin/bwrap", "--info-fd", "3", "--block-fd", "4", "--seccomp", "5", "--unshare-pid", "--", "/usr/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: 200001, Gid: 200002, Groups: []uint32{300001}}}
	cmd.Env = []string{"HOME=/workspace", "PATH=/usr/bin"}
	cmd.Dir = "/workspace"
	for range 3 {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		cmd.ExtraFiles = append(cmd.ExtraFiles, f)
	}
	return cmd
}

func TestWrapPreservesTrackedFDsArgvAndIdentity(t *testing.T) {
	h := testHandle(t, &recorder{})
	h.ready = true
	cmd := trackedCommand(t)
	beforeArgs := append([]string(nil), cmd.Args...)
	beforeFiles := append([]*os.File(nil), cmd.ExtraFiles...)
	beforeEnv := append([]string(nil), cmd.Env...)
	if err := h.wrap(cmd, 200001, 200002, []uint32{300001}); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != nsenterTool || !reflect.DeepEqual(cmd.Args[len(cmd.Args)-len(beforeArgs):], beforeArgs) {
		t.Fatalf("lost argv: %v", cmd.Args)
	}
	if !reflect.DeepEqual(cmd.ExtraFiles, beforeFiles) || !reflect.DeepEqual(cmd.Env, beforeEnv) || cmd.Dir != "/workspace" || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Credential != nil {
		t.Fatal("changed tracked descriptors/process settings")
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--no-fork --net=" + h.NamespacePath() + " -- " + setprivTool, "--reuid 200001 --regid 200002 --groups 300001", "--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q: %s", want, joined)
		}
	}
}

func TestWrapRejectsRootMismatchedUnknownAndUntrackedIdentities(t *testing.T) {
	h := testHandle(t, &recorder{})
	h.ready = true
	for _, change := range []func(*exec.Cmd){
		func(c *exec.Cmd) { c.SysProcAttr.Credential.Uid = 0 },
		func(c *exec.Cmd) { c.SysProcAttr.Credential.Uid = 65534 },
		func(c *exec.Cmd) { c.SysProcAttr.Credential.Gid = 0 },
		func(c *exec.Cmd) { c.SysProcAttr.Credential.Groups = []uint32{300002} },
		func(c *exec.Cmd) { c.SysProcAttr.Credential.NoSetGroups = true },
		func(c *exec.Cmd) { c.SysProcAttr.Credential = nil },
		func(c *exec.Cmd) { c.SysProcAttr.Setpgid = false },
		func(c *exec.Cmd) { c.ExtraFiles = c.ExtraFiles[:2] },
		func(c *exec.Cmd) { c.Args[2] = "9" },
		func(c *exec.Cmd) { c.Path = "/usr/bin/sh" },
	} {
		cmd := trackedCommand(t)
		change(cmd)
		before := append([]string(nil), cmd.Args...)
		if err := h.wrap(cmd, 200001, 200002, []uint32{300001}); err == nil {
			t.Fatalf("accepted invalid command: %+v", cmd)
		}
		if !reflect.DeepEqual(cmd.Args, before) {
			t.Fatal("rejection mutated command")
		}
	}
	for _, groups := range [][]uint32{nil, {0}, {65534}, {^uint32(0)}} {
		if err := h.wrap(trackedCommand(t), 200001, 200002, groups); err == nil {
			t.Fatal("accepted unknown group", groups)
		}
	}
	h.ready = false
	if err := h.wrap(trackedCommand(t), 200001, 200002, []uint32{300001}); err == nil {
		t.Fatal("accepted unready handle")
	}
}

func TestHelperRestrictedIdentityAndNoInheritedBusinessFDs(t *testing.T) {
	h := testHandle(t, &recorder{})
	h.ready = true
	cmd, err := h.helperCommand(context.Background(), "/usr/bin/cm-slot", "netguard-relay", "--parent", h.ParentIP())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--no-fork --net=" + h.NamespacePath(), "--reuid 65534 --regid 65534 --clear-groups", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "-- /usr/bin/cm-slot netguard-relay --parent " + h.ParentIP()} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q: %s", want, joined)
		}
	}
	if len(cmd.ExtraFiles) != 0 || cmd.SysProcAttr.Credential != nil || !cmd.SysProcAttr.Setpgid || len(cmd.Env) == 0 {
		t.Fatal("helper inherited business/ambient settings")
	}
	for _, argv := range [][]string{nil, {"relative-helper"}, {"/usr/bin/../bin/helper"}, {"/usr/bin/helper\x00"}} {
		if _, err := h.helperCommand(context.Background(), argv...); err == nil {
			t.Fatal("accepted unsafe helper argv", argv)
		}
	}
	h.ready = false
	if _, err := h.helperCommand(context.Background(), "/usr/bin/helper"); err == nil {
		t.Fatal("accepted unready helper")
	}
}

func TestPublicEntrypointsRejectNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux contract")
	}
	h := testHandle(t, &recorder{})
	h.ready = true
	if got, err := Prepare(context.Background(), testConfig(t), Identity{1, 1}); err == nil || got != nil || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("prepare: %v %v", got, err)
	}
	if err := h.Wrap(trackedCommand(t), 200001, 200002, []uint32{300001}); err == nil {
		t.Fatal("Wrap succeeded on non-Linux")
	}
	if _, err := h.HelperCommand(context.Background(), "/usr/bin/helper"); err == nil {
		t.Fatal("helper succeeded on non-Linux")
	}
}
