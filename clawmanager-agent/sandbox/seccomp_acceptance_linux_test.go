//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestSeccompKernelProbeHelper(t *testing.T) {
	if os.Getenv("CM_SECCOMP_KERNEL_PROBE") != "1" {
		t.Skip("sandbox-only kernel probe")
	}
	var mount, unshare uintptr
	switch runtime.GOARCH {
	case "amd64":
		mount, unshare = 165, 272
	case "arm64":
		mount, unshare = 40, 97
	default:
		t.Fatal("unsupported acceptance architecture")
	}
	for _, number := range []uintptr{mount, unshare} {
		if _, _, errno := syscall.RawSyscall(number, 0, 0, 0); errno != syscall.EPERM {
			t.Fatalf("restricted syscall %d returned %v, want EPERM", number, errno)
		}
	}
	if _, _, errno := syscall.RawSyscall(435, 0, 0, 0); errno != syscall.ENOSYS {
		t.Fatalf("clone3 returned %v, want safe libc fallback ENOSYS", errno)
	}
}

func TestLinuxSeccompKernelGate(t *testing.T) {
	if os.Getenv("CM_SANDBOX_ACCEPTANCE") != "1" {
		t.Skip("run the real nonroot Linux acceptance gate")
	}
	if os.Getuid() == 0 || os.Getgid() == 0 {
		t.Fatal("run as the intended nonroot gateway UID/GID")
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "sandbox.test"), contents, 0755); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Args: []string{"/workspace/sandbox.test", "-test.run=^TestSeccompKernelProbeHelper$", "-test.v"}, Env: []string{"CM_SECCOMP_KERNEL_PROBE=1", "PATH=/usr/bin:/bin", "HOME=/workspace"}, Dir: "/workspace", UID: os.Getuid(), GID: os.Getgid(), Mounts: []Mount{{Source: workspace, Target: "/workspace"}}}
	cmd := acceptanceCommand(t, spec)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	tracker, err := Track(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = tracker.Started(ctx); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err = tracker.WaitStopped(ctx); err != nil {
		t.Fatal(err)
	}
}
