//go:build linux

package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestGracefulProcessHelper(t *testing.T) {
	mode := os.Getenv("CM_STOP_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Println("ready")
	for {
		time.Sleep(time.Second)
	}
}

func startStopHelper(t *testing.T, mode string, pgid int) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGracefulProcessHelper$")
	cmd.Env = append(os.Environ(), "CM_STOP_TEST_HELPER="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("helper failed to exit")
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper readiness %q: %v", line, err)
	}
	return cmd, exited
}

func TestGracefulStopRealProcessAndIdempotence(t *testing.T) {
	cmd, exited := startStopHelper(t, "normal", 0)
	stop := newGracefulGatewayStop(cmd, exited)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := stop(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGracefulStopDoesNotKillOnTimeout(t *testing.T) {
	cmd, exited := startStopHelper(t, "ignore", 0)
	stop := newGracefulGatewayStop(cmd, exited)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := stop(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-exited:
		t.Fatal("timeout force-killed process")
	default:
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatal("process no longer alive", err)
	}
}

func TestGracefulStopWaitsForEntireProcessGroup(t *testing.T) {
	leader, leaderExited := startStopHelper(t, "normal", 0)
	follower, followerExited := startStopHelper(t, "ignore", leader.Process.Pid)
	stop := newGracefulGatewayStop(leader, leaderExited)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	err := stop(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("surviving group certified: %v", err)
	}
	select {
	case <-leaderExited:
	default:
		t.Fatal("leader did not exit")
	}
	select {
	case <-followerExited:
		t.Fatal("follower force-killed")
	default:
	}
	// Test cleanup simulates the outstanding child finishing independently.
	if err := follower.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-followerExited
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatalf("retry failed after group exit: %v", err)
	}
}
