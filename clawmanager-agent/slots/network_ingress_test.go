package slots

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeIngressRejectsWrongTokenBeforeDial(t *testing.T) {
	var opens atomic.Int32
	p := newNativeIngress(context.Background(), "ui1_native-secret", func(context.Context) (net.Conn, error) { opens.Add(1); return nil, io.EOF })
	defer p.Close()
	for _, token := range []string{"", "wrong", "ui1_native-secretx"} {
		r := httptest.NewRequest("GET", "http://native/", nil)
		r.RemoteAddr = "10.1.2.3:1234"
		r.Header.Set("X-ClawManager-Ingress-Token", token)
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		out := httptest.NewRecorder()
		p.ServeHTTP(out, r)
		if out.Code != 401 {
			t.Fatalf("token %q status=%d", token, out.Code)
		}
	}
	if opens.Load() != 0 {
		t.Fatal("rejected ingress opened Unix/native connection")
	}
}
func TestNativeIngressUpgradePreservesBufferedBytesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan *http.Request, 1)
	p := newNativeIngress(ctx, "ui1_native-secret", func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			r := bufio.NewReader(b)
			req, err := http.ReadRequest(r)
			if err != nil {
				return
			}
			received <- req
			io.WriteString(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nserver")
			payload := make([]byte, 6)
			if _, err = io.ReadFull(r, payload); err == nil {
				b.Write(payload)
			}
			io.Copy(io.Discard, r)
		}()
		return a, nil
	})
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: native\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nX-ClawManager-Ingress-Token: ui1_native-secret\r\n\r\nclient")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	req := <-received
	if req.Header.Get("X-ClawManager-Ingress-Token") != "" {
		t.Fatal("outer ingress credential reached native")
	}
	b := make([]byte, 12)
	if _, err = io.ReadFull(reader, b); err != nil || string(b) != "serverclient" {
		t.Fatalf("buffered upgrade data %q %v", b, err)
	}
	cancel()
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upgrade cleanup did not join")
	}
}

func TestNativeIngressStreamsAndCancellationJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan *http.Request, 1)
	p := newNativeIngress(ctx, "ui1_native-secret", func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			reader := bufio.NewReader(b)
			r, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			got <- r
			io.WriteString(b, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n9\r\ndata: 1\n\n\r\n")
			io.Copy(io.Discard, reader)
		}()
		return a, nil
	})
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload := make([]byte, 9)
	if _, err = io.ReadFull(response.Body, payload); err != nil || string(payload) != "data: 1\n\n" {
		t.Fatalf("SSE did not flush: %q %v", payload, err)
	}
	if (<-got).Header.Get(nativeIngressHeader) != "" {
		t.Fatal("ingress token leaked")
	}
	cancel()
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE cancellation did not join")
	}
}

func TestSessionHTTPListenerBoundsAndClosesIdleConnections(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newSessionHTTPListener(raw, make(chan struct{}, 1))
	defer listener.Close()
	client, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	extra, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	accepting := make(chan net.Conn, 1)
	go func() { c, _ := listener.Accept(); accepting <- c }()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = extra.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection above budget was admitted")
	}
	accepted.Close()
	next, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	select {
	case c := <-accepting:
		if c == nil {
			t.Fatal("closed connection did not return budget")
		}
	case <-time.After(time.Second):
		t.Fatal("connection budget leaked")
	}
	listener.Close()
	next.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = next.Read(make([]byte, 1)); err == nil {
		t.Fatal("session shutdown retained idle socket")
	}
}
