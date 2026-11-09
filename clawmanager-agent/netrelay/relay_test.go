package netrelay

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestTransparentConnectPreservesBufferedPayloadAndClosesOnCancel(t *testing.T) {
	broker := tcpListener(t)
	received := make(chan *http.Request, 1)
	brokerDone := make(chan struct{})
	go func() {
		defer close(brokerDone)
		c, err := broker.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		received <- r
		io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\nearly")
		io.Copy(c, c)
	}()
	l := tcpListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, Config{Broker: broker.Addr().String()}, l, nil,
			func(net.Conn) (string, error) { return "[2606:4700:4700::1111]:443", nil })
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 5)
	if _, err = io.ReadFull(c, b); err != nil || string(b) != "early" {
		t.Fatalf("buffered payload %q %v", b, err)
	}
	r := <-received
	if r.Method != "CONNECT" || r.Host != "[2606:4700:4700::1111]:443" || len(r.Header) != 0 {
		t.Fatalf("unexpected request: %#v", r)
	}
	io.WriteString(c, "hello")
	if _, err = io.ReadFull(c, b); err != nil || string(b) != "hello" {
		t.Fatalf("echo %q %v", b, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay retained connections")
	}
	select {
	case <-brokerDone:
	case <-time.After(time.Second):
		t.Fatal("upstream retained")
	}
}

func TestRejectedConnectDoesNotForwardBody(t *testing.T) {
	broker := tcpListener(t)
	go func() {
		c, e := broker.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		http.ReadRequest(bufio.NewReader(c))
		io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 6\r\n\r\nsecret")
	}()
	l := tcpListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, Config{Broker: broker.Addr().String()}, l, nil, func(net.Conn) (string, error) { return "8.8.8.8:443", nil })
	}()
	c, e := net.Dial("tcp", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	b, e := io.ReadAll(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != 0 {
		t.Fatalf("leaked rejection: %q", b)
	}
	cancel()
	<-done
}

func TestConfigRejectsNonLiteralBrokerAndInvalidIngress(t *testing.T) {
	for _, cfg := range []Config{{Broker: "evil.example:31280"}, {Broker: "127.0.0.1:0"}, {Broker: "169.254.64.1:31280", IngressPorts: []int{0}}, {Broker: "169.254.64.1:31280", IngressPorts: []int{80, 80}}} {
		if err := cfg.validate(); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	if err := (Config{Broker: "169.254.64.1:31280", IngressPorts: []int{8080, 8081}}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidOriginalDestinationCannotInjectHeaders(t *testing.T) {
	for _, target := range []string{"evil.example:443", "8.8.8.8:443\r\nAuthorization: secret", "8.8.8.8:0", "/api"} {
		if validAddress(target) {
			t.Fatal("accepted " + strings.ReplaceAll(target, "\r\n", " "))
		}
	}
}

func TestIngressUsesFixedPrivateLoopbackAndPreservesHalfClose(t *testing.T) {
	upstream := tcpListener(t)
	go func() {
		c, e := upstream.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		c.Write(append([]byte("reply:"), b...))
	}()
	dir, e := os.MkdirTemp("/tmp", "cmrelay-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	l, e := net.Listen("unix", filepath.Join(dir, "ingress.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	port := upstream.Addr().(*net.TCPAddr).Port
	go func() {
		done <- Run(ctx, Config{Broker: "169.254.64.1:31280", IngressPorts: []int{port}}, tcpListener(t), []net.Listener{l})
	}()
	c, e := net.Dial("unix", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(c, "payload")
	c.(*net.UnixConn).CloseWrite()
	b, e := io.ReadAll(c)
	if e != nil || string(b) != "reply:payload" {
		t.Fatalf("half-close: %q %v", b, e)
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
