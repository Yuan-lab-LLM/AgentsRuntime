//go:build linux

package netguard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/netrelay"
)

func TestMain(m *testing.M) {
	if handled, err := netrelay.RunCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	if handled, err := RunRootlessCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRootlessRejectsMalformedDescriptorBundle(t *testing.T) {
	for _, count := range []int{0, 1, 7, 8, 9} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			a, b := os.NewFile(uintptr(pair[0]), "a"), os.NewFile(uintptr(pair[1]), "b")
			left, err := net.FileConn(a)
			a.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer left.Close()
			right, err := net.FileConn(b)
			b.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer right.Close()
			f, err := os.Open("/dev/null")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			fds := make([]int, count)
			for i := range fds {
				fds[i] = int(f.Fd())
			}
			_, _, err = left.(*net.UnixConn).WriteMsgUnix([]byte(`{"Parent":"169.254.64.1","Child":"169.254.64.2"}`), syscall.UnixRights(fds...), nil)
			if err != nil {
				t.Fatal(err)
			}
			h := &RootlessHandle{control: right.(*net.UnixConn), parent: "169.254.64.1", child: "169.254.64.2"}
			if err = h.receive(); err == nil {
				t.Fatal("accepted malformed control descriptors")
			}
		})
	}
}

func TestRootlessLostKeeperCannotConfirmCleanup(t *testing.T) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(pair[0]), "control")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = syscall.Close(pair[1])
	h := &RootlessHandle{control: conn.(*net.UnixConn)}
	if err = h.Close(context.Background()); !errors.Is(err, ErrCleanupUnconfirmed) {
		t.Fatalf("lost keeper released ownership: %v", err)
	}
	if h.closed {
		t.Fatal("uncertain network was marked closed")
	}
}

func TestRootlessRejectsWrongSupervisorIdentity(t *testing.T) {
	if os.Geteuid() == 10001 {
		t.Skip("already using required management identity")
	}
	if _, err := PrepareRootless(context.Background(), Config{}, Identity{}, 200123, 200123, nil, "/usr/local/bin/agent"); err == nil || !strings.Contains(err.Error(), "management identity") {
		t.Fatalf("accepted wrong supervisor: %v", err)
	}
}

// Run in an ordinary disposable Pod as management UID10001 with only
// SETUID/SETGID, AppArmor Unconfined and the image's bubblewrap allowance.
func TestRootlessNetworkReal(t *testing.T) {
	if os.Getenv("CM_ROOTLESS_ACCEPTANCE") != "1" {
		t.Skip("requires disposable ordinary Pod")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := PrepareRootless(ctx, Config{SlotIndex: 0, SlotCount: 2, StateDir: "/tmp/network-test", PodUID: "rootless-test"}, Identity{InstanceID: 1, Generation: 1}, 200123, 200123, []uint32{400123}, executable)
	if err != nil {
		t.Fatal(err)
	}
	listeners, udp := h.BrokerListeners()
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
		udp.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	accepted := make(chan error, 1)
	go func() {
		c, e := listeners[0].Accept()
		if e == nil {
			defer c.Close()
			b := make([]byte, 4)
			_, e = io.ReadFull(c, b)
			if e == nil && string(b) != "ping" {
				e = fmt.Errorf("wrong TCP payload")
			}
			if e == nil {
				_, e = c.Write([]byte("pong"))
			}
		}
		accepted <- e
	}()
	command, err := h.HelperCommand(ctx, nil, "/usr/bin/python3", "-c", `import socket,os; assert os.getuid()==65534; s=socket.create_connection(("`+h.ParentIP()+`",31280));s.sendall(b"ping");assert s.recv(4)==b"pong"`)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("namespace helper: %v: %s", err, out)
	}
	if err = <-accepted; err != nil {
		t.Fatal(err)
	}
	// The bound UDP socket also preserves the business peer's source address.
	packets := make(chan error, 1)
	go func() {
		b := make([]byte, 32)
		n, peer, e := udp.ReadFrom(b)
		if e == nil && string(b[:n]) != "dns-probe" {
			e = fmt.Errorf("wrong DNS payload")
		}
		if e == nil && !strings.HasPrefix(peer.String(), h.ChildIP()+":") {
			e = fmt.Errorf("wrong DNS source: %s", peer)
		}
		if e == nil {
			_, e = udp.WriteTo([]byte("dns-reply"), peer)
		}
		packets <- e
	}()
	command, err = h.HelperCommand(ctx, nil, "/usr/bin/python3", "-c", `import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(3);s.sendto(b"dns-probe",("`+h.ParentIP()+`",53));assert s.recv(32)==b"dns-reply"`)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("DNS helper: %v: %s", err, out)
	}
	if err = <-packets; err != nil {
		t.Fatal(err)
	}
	// Existing netrelay still intercepts arbitrary IPv4/IPv6 TCP destinations.
	relay, err := h.HelperCommand(ctx, nil, executable, netrelay.Command)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(netrelay.Config{Broker: net.JoinHostPort(h.ParentIP(), "31280")})
	relay.Stdin = strings.NewReader(string(data))
	relay.Stderr = os.Stderr
	output, err := relay.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = relay.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := relay.Process.Kill(); err != nil {
			t.Error("management cannot stop isolated relay", err)
		}
		_ = relay.Wait()
	}()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "{\"ready\":true}\n" {
		t.Fatalf("relay readiness: %q %v", line, err)
	}
	for _, destination := range []string{"203.0.113.123", "2001:db8::123"} {
		accepted := make(chan error, 1)
		go func() {
			c, e := listeners[0].Accept()
			if e == nil {
				defer c.Close()
				reader := bufio.NewReader(c)
				line, readErr := reader.ReadString('\n')
				e = readErr
				if e == nil && !strings.HasPrefix(line, "CONNECT "+net.JoinHostPort(destination, "443")+" ") {
					e = fmt.Errorf("wrong transparent target: %q", line)
				}
				if e == nil {
					for {
						line, e = reader.ReadString('\n')
						if e != nil || line == "\r\n" {
							break
						}
					}
				}
				if e == nil {
					_, e = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				}
				if e == nil {
					b := make([]byte, 4)
					_, e = io.ReadFull(reader, b)
					if e == nil && string(b) != "ping" {
						e = fmt.Errorf("wrong transparent body")
					}
					if e == nil {
						_, e = c.Write([]byte("pong"))
					}
				}
			}
			accepted <- e
		}()
		argv := append([]string{setprivTool}, privilegeArgs(200123, 200123, []uint32{400123})...)
		argv = append(argv, "/usr/bin/python3", "-c", `import socket; s=socket.create_connection(("`+destination+`",443),3);s.sendall(b"ping");assert s.recv(4)==b"pong"`)
		client := exec.CommandContext(ctx, nsenterTool, rootlessEnterArgs(3, 4, argv)...)
		client.ExtraFiles = []*os.File{h.user, h.network}
		client.Env = []string{"PATH=/usr/bin:/bin"}
		if out, err := client.CombinedOutput(); err != nil {
			t.Fatalf("transparent %s: %v: %s", destination, err, out)
		}
		if err = <-accepted; err != nil {
			t.Fatal(err)
		}
	}
	// A caller in the outer Pod cannot connect to the private broker address.
	direct, err := net.DialTimeout("tcp", net.JoinHostPort(h.ParentIP(), "31280"), 200*time.Millisecond)
	if err == nil {
		direct.Close()
		t.Fatal("private broker leaked into Pod network")
	}
}

func TestRootlessLocalhostIsolationReal(t *testing.T) {
	if os.Getenv("CM_ROOTLESS_ACCEPTANCE") != "1" {
		t.Skip("requires disposable ordinary Pod")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outer, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	outerPort := outer.Addr().(*net.TCPAddr).Port
	var handles []*RootlessHandle
	for index := 0; index < 2; index++ {
		h, err := PrepareRootless(ctx, Config{SlotIndex: 0, SlotCount: 2, StateDir: "/tmp/network-test", PodUID: "rootless-test"}, Identity{InstanceID: index + 1, Generation: 1}, 200123+index, 200123+index, nil, executable)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
		listeners, udp := h.BrokerListeners()
		var server *exec.Cmd
		defer func() {
			if server != nil && server.Process != nil {
				if err := server.Process.Kill(); err != nil {
					t.Error("manager cannot kill namespace-owned helper", err)
				}
				_ = server.Wait()
			}
			for _, l := range listeners {
				l.Close()
			}
			udp.Close()
			if err := h.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		code := fmt.Sprintf(`import socket,select
a=socket.socket();a.bind(("127.0.0.1",18888));a.listen()
b=socket.socket(socket.AF_INET6);b.bind(("::1",18888));b.listen()
c=socket.socket(socket.AF_UNIX);c.bind("\x00cm-localhost-test");c.listen()
print("ready",flush=True)
while True:
 for s in select.select([a,b,c],[],[])[0]:
  p,_=s.accept();p.sendall(b"instance-%d");p.close()
`, index)
		server, err = h.HelperCommand(ctx, nil, "/usr/bin/python3", "-c", code)
		if err != nil {
			t.Fatal(err)
		}
		server.Stderr = os.Stderr
		output, err := server.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = server.Start(); err != nil {
			t.Fatal(err)
		}
		if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("local server readiness: %q %v", line, err)
		}
	}
	first, err := handles[0].network.Stat()
	if err != nil {
		t.Fatal(err)
	}
	second, err := handles[1].network.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Fatal("executions share network namespace")
	}
	for index, h := range handles {
		code := fmt.Sprintf(`import socket
for family,target in [(socket.AF_INET,("127.0.0.1",18888)),(socket.AF_INET6,("::1",18888)),(socket.AF_UNIX,"\x00cm-localhost-test")]:
 s=socket.socket(family);s.settimeout(2);s.connect(target);assert s.recv(64)==b"instance-%d";s.close()
s=socket.socket();s.settimeout(.2)
try: s.connect(("127.0.0.1",%d));raise AssertionError("Pod localhost reachable")
except OSError: pass
`, index, outerPort)
		client, err := h.HelperCommand(ctx, nil, "/usr/bin/python3", "-c", code)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := client.CombinedOutput(); err != nil {
			t.Fatalf("localhost separation: %v: %s", err, out)
		}
	}
}

func TestRootlessMappingsExcludeOuterRootAndOtherInstances(t *testing.T) {
	uids, gids, err := rootlessMappings(200123, 200123, []uint32{400123})
	if err != nil {
		t.Fatal(err)
	}
	if uids != "0 10001 1\n65534 65534 1\n200123 200123 1\n" {
		t.Fatalf("unexpected UID grants: %q", uids)
	}
	if gids != uids+"400123 400123 1\n" {
		t.Fatalf("unexpected GID grants: %q", gids)
	}
	for _, tc := range []struct {
		uid, gid int
		groups   []uint32
	}{
		{0, 200123, nil}, {10001, 200123, nil}, {200123, 0, nil},
		{200123, 10001, nil}, {200123, 65534, nil},
		{200123, 200123, []uint32{10001}}, {200123, 200123, []uint32{0}},
		{200123, 200123, []uint32{65534}}, {200123, 200123, []uint32{400123, 400123}},
	} {
		if _, _, err := rootlessMappings(tc.uid, tc.gid, tc.groups); err == nil {
			t.Fatalf("accepted invalid identity: %+v", tc)
		}
	}
}

func TestRootlessNamespaceDescriptorsClosedBeforeBusinessExec(t *testing.T) {
	args := rootlessEnterArgs(6, 7, []string{"/usr/bin/setpriv", "--", "/usr/bin/bwrap"})
	if len(args) < 8 || args[0] != "--no-fork" || args[1] != "--user=/proc/self/fd/6" || args[2] != "--net=/proc/self/fd/7" {
		t.Fatalf("unbound namespace entry: %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `exec 6<&- 7<&-; exec "$@"`) {
		t.Fatalf("control FDs would reach business: %v", args)
	}
}
