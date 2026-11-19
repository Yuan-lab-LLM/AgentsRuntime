package netrelay

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreviewPrivateConnectHTTPAndCleanup(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte{0, 1, 255}) }))
	defer app.Close()
	dir, err := os.MkdirTemp("/tmp", "preview-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "preview.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithPreview(ctx, Config{Broker: "127.0.0.1:31280", Preview: true, IngressPorts: []int{20000}}, tcpListener(t), []net.Listener{mustUnix(t, dir, "native.sock")}, listener)
	}()
	conn, err := DialPreview(ctx, listener.Addr().String(), app.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != string([]byte{0, 1, 255}) {
		t.Fatalf("body %v %v", body, err)
	}
	for _, target := range []string{"8.8.8.8:80", "localhost:80", "127.0.0.1:0", "127.0.0.1:080", "127.0.0.1:15000", "[::1]:20000", "127.0.0.1:31280", "127.0.0.1:80\r\nX: x"} {
		if c, e := DialPreview(ctx, listener.Addr().String(), target); e == nil {
			c.Close()
			t.Errorf("accepted %q", target)
		}
	}
	// An incomplete handshake must not hold teardown open.
	slow, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("helper retained preview")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("preview remained open")
	}
}
func mustUnix(t *testing.T, dir, name string) net.Listener {
	t.Helper()
	l, e := net.Listen("unix", filepath.Join(dir, name))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestPreviewPropagatesApplicationEOF(t *testing.T) {
	app := tcpListener(t)
	go func() {
		c, e := app.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "response")
	}()
	dir, e := os.MkdirTemp("/tmp", "preview-eof-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	listener := mustUnix(t, dir, "helper.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithPreview(ctx, Config{Broker: "127.0.0.1:31280", Preview: true}, tcpListener(t), nil, listener)
	}()
	c, e := DialPreview(ctx, listener.Addr().String(), app.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	b, e := io.ReadAll(c)
	if e != nil || string(b) != "response" {
		t.Errorf("application EOF lost: %q %v", b, e)
	}
	c.Close()
	cancel()
	<-done
}

func TestPreviewRejectsExtraHeadersAndOversizedPrelude(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "preview-invalid-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener := mustUnix(t, dir, "helper.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithPreview(ctx, Config{Broker: "127.0.0.1:31280", Preview: true}, tcpListener(t), nil, listener)
	}()
	for _, prelude := range []string{"CONNECT 127.0.0.1:3000 HTTP/1.1\r\nHost: 127.0.0.1:3000\r\nContent-Length: 1\r\n\r\nx", "CONNECT 127.0.0.1:3000 HTTP/1.1\r\nHost: localhost:3000\r\n\r\n", strings.Repeat("a", 1025)} {
		c, err := net.Dial("unix", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		io.WriteString(c, prelude)
		b, err := io.ReadAll(c)
		c.Close()
		if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("unbounded malformed handshake")
		}
		if len(b) != 0 {
			t.Fatalf("accepted prelude: %q", b)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
