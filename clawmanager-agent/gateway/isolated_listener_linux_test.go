//go:build linux

package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestIsolatedListenerHelper(t *testing.T) {
	if os.Getenv("CLAWMANAGER_TEST_LISTENER_CHILD") != "true" {
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	_, _ = io.Copy(io.Discard, os.Stdin)
	listener.Close()
	os.Exit(0)
}

func spawnIsolatedListener(t *testing.T) (int, int) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestIsolatedListenerHelper$")
	command.Env = append(os.Environ(), "CLAWMANAGER_TEST_LISTENER_CHILD=true")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("listener child did not publish its port")
	}
	port, err := strconv.Atoi(scanner.Text())
	if err != nil {
		t.Fatal(err)
	}
	return command.Process.Pid, port
}

func TestVerifyIsolatedListenerRequiresMatchingProcessGroup(t *testing.T) {
	firstPID, firstPort := spawnIsolatedListener(t)
	secondPID, secondPort := spawnIsolatedListener(t)
	for _, pair := range [][2]int{{firstPID, firstPort}, {secondPID, secondPort}} {
		if err := validateIsolatedListener(pair[0], pair[1]); err != nil {
			t.Fatalf("managed socket rejected: %v", err)
		}
	}
	if err := validateIsolatedListener(firstPID, secondPort); err == nil {
		t.Fatal("other instance listener was accepted")
	}
	if err := validateIsolatedListener(secondPID, firstPort); err == nil {
		t.Fatal("other process group listener was accepted")
	}
	if err := validateIsolatedListener(0, firstPort); err == nil {
		t.Fatal("missing process was accepted")
	}
}

func TestListenerInspectionCommandAcceptsOnlyFixedArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"llm-config"}, {"dsh-web-proxy"}} {
		if handled, err := RunListenerProbe(args); handled || err != nil {
			t.Fatalf("unrelated command was handled: %v", args)
		}
	}
	for _, args := range [][]string{
		{"--verify-hermes-listener"},
		{"--verify-hermes-listener", "2"},
		{"--verify-hermes-listener", "2", "20000", "unexpected"},
		{"--verify-hermes-listener", "0", "20000"},
		{"--verify-hermes-listener", "1", "20000"},
		{"--verify-hermes-listener", "-2", "20000"},
		{"--verify-hermes-listener", "02", "20000"},
		{"--verify-hermes-listener", "2", "65536"},
		{"--verify-hermes-listener", "2", "0"},
		{"--verify-hermes-listener", "private-token", "20000"},
	} {
		handled, err := RunListenerProbe(args)
		if !handled || err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatal("invalid helper command did not fail with a sanitized error")
		}
	}
	pid, port := spawnIsolatedListener(t)
	if handled, err := RunListenerProbe([]string{"--verify-hermes-listener", strconv.Itoa(pid), strconv.Itoa(port)}); !handled || err != nil {
		t.Fatalf("same-UID inspection command rejected a real owned listener: %v", err)
	}
}

func TestListenerVerificationRejectsMismatchedUIDBeforeFallback(t *testing.T) {
	pid, port := spawnIsolatedListener(t)
	starter := NewExecProcessStarter(Config{})
	if err := starter.VerifyListener(GatewayStartSpec{UID: os.Getuid() + 1, GID: 10001, Port: port}, pid); err == nil {
		t.Fatal("listener inspection accepted another instance UID")
	}
	if !listenerProcessOwnedBy(pid, os.Getuid()) {
		t.Fatal("spawned listener owner does not match its real UID")
	}
}

func TestCollectListenerInodesFiltersEstablishedAndOtherPorts(t *testing.T) {
	table := "  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n" +
		"0: 0100007F:4E20 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 12345 1\n" +
		"1: 0100007F:4E20 00000000:0000 01 00000000:00000000 00:00000000 00000000 1000 0 67890 1\n" +
		"2: 0100007F:4E21 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 98765 1\n"
	inodes := map[string]bool{}
	if err := collectListenerInodes(bufio.NewScanner(strings.NewReader(table)), 20000, inodes); err != nil {
		t.Fatal(err)
	}
	if len(inodes) != 1 {
		t.Fatalf("listener identities = %v", inodes)
	}
	if _, ok := inodes["12345"]; !ok {
		t.Fatal("matching listener inode not found")
	}
}
