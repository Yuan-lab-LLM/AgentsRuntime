package netbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func brokerBackend(t *testing.T, h http.HandlerFunc) *httptest.Server {
	return leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/security/lease" {
			json.NewEncoder(w).Encode(testLease())
			return
		}
		h(w, r)
	})
}
func brokerForTest(t *testing.T, backend, proxy string) *Broker {
	t.Helper()
	c, err := NewCredentials(context.Background(), backend, "control-sentinel", testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	b, err := New(context.Background(), Config{ChildIP: "127.0.0.1", BackendURL: backend, EgressProxyURL: proxy, Credentials: c})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}
func localRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:34567"
	return r
}
func TestModelFixedRouteCurrentCredentialAndHeaders(t *testing.T) {
	seen := make(chan *http.Request, 2)
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		fmt.Fprint(w, "model-output")
	})
	b := brokerForTest(t, backend.URL, backend.URL)
	for i, token := range []string{"v1.model", "v1.rotated"} {
		if i == 1 {
			b.cfg.Credentials.(*Credentials).mu.Lock()
			b.cfg.Credentials.(*Credentials).lease.ModelToken = token
			b.cfg.Credentials.(*Credentials).mu.Unlock()
		}
		r := localRequest("POST", "/api/v1/gateway/llm/v1/messages?stream=1")
		for k, v := range map[string]string{"Authorization": "Bearer local-dummy", "X-Api-Key": "local-dummy-key", "Proxy-Authorization": "Bearer forged", "X-ClawManager-Control-Token": "evil-control", "X-ClawManager-Instance-Id": "999", "Forwarded": "for=evil", "X-Forwarded-For": "evil", "Connection": "X-Secret", "X-Secret": "sentinel"} {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		b.Model().ServeHTTP(rec, r)
		if rec.Code != 200 || rec.Body.String() != "model-output" {
			t.Fatalf("model status=%d body=%s", rec.Code, rec.Body.String())
		}
		got := <-seen
		if got.URL.Path != "/api/v1/gateway/llm/v1/messages" || got.URL.RawQuery != "stream=1" || got.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("wrong fixed route/model credential: %s %s", got.URL.Path, got.Header.Get("Authorization"))
		}
		for _, key := range []string{"Proxy-Authorization", "X-Api-Key", "X-ClawManager-Control-Token", "X-ClawManager-Instance-Id", "Forwarded", "X-Forwarded-For", "X-Secret"} {
			if got.Header.Get(key) != "" {
				t.Errorf("forged header forwarded: %s", key)
			}
		}
	}
}
func TestModelRejectsRouteEscapeAndWrongChild(t *testing.T) {
	var calls atomic.Int32
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	b := brokerForTest(t, backend.URL, backend.URL)
	for _, path := range []string{"/api/v1/runtime/security/lease", "/api/v1/gateway/llmevil", "/api/v1/gateway/llm/../admin", "/api/v1/gateway/llm/%2e%2e/admin", "http://attacker.invalid/api/v1/gateway/llm/models"} {
		rec := httptest.NewRecorder()
		b.Model().ServeHTTP(rec, localRequest("GET", path))
		if rec.Code < 400 {
			t.Errorf("escaped model route accepted: %q", path)
		}
	}
	r := localRequest("GET", "/api/v1/gateway/llm/models")
	r.RemoteAddr = "127.0.0.2:4444"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	b.Model().ServeHTTP(rec, r)
	if rec.Code != 403 || calls.Load() != 0 {
		t.Fatalf("wrong source routed: status=%d calls=%d", rec.Code, calls.Load())
	}
}
func TestEgressHTTPOnlyFixedProxyReceivesCapability(t *testing.T) {
	seen := make(chan *http.Request, 1)
	proxy := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		fmt.Fprint(w, "public-output")
	})
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) { t.Error("egress reached backend API") })
	b := brokerForTest(t, backend.URL, proxy.URL)
	r := localRequest("GET", "http://origin.invalid/path")
	r.Header.Set("Proxy-Authorization", "Bearer attacker")
	r.Header.Set("Authorization", "Bearer public-origin-key")
	r.Header.Set("X-ClawManager-Instance-Id", "999")
	r.Header.Set("X-Forwarded-For", "evil")
	rec := httptest.NewRecorder()
	b.Egress().ServeHTTP(rec, r)
	if rec.Code != 200 || rec.Body.String() != "public-output" {
		t.Fatalf("egress status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := <-seen
	if got.URL.Scheme != "http" || got.URL.Host != "origin.invalid" || got.Header.Get("Proxy-Authorization") != "Bearer v1.egress" || got.Header.Get("Authorization") != "Bearer public-origin-key" {
		t.Fatalf("wrong proxy request URL=%s", got.URL)
	}
	if got.Header.Get("X-ClawManager-Instance-Id") != "" || got.Header.Get("X-Forwarded-For") != "" {
		t.Fatal("forged identity reached proxy")
	}
	for _, target := range []string{"/relative", "https://origin.invalid/path", "http://user:secret@origin.invalid/"} {
		rec := httptest.NewRecorder()
		b.Egress().ServeHTTP(rec, localRequest("GET", target))
		if rec.Code < 400 {
			t.Errorf("unsupported egress target accepted: %s", target)
		}
	}
}
func rawUpstream(t *testing.T, serve func(net.Conn)) *net.TCPListener {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err == nil {
			defer conn.Close()
			serve(conn)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fake proxy worker did not stop")
		}
	})
	return l
}
func brokerServer(t *testing.T, b *Broker) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(b.Egress())
	t.Cleanup(func() { b.Close(); s.Close() })
	return s
}
func TestConnectBufferedBothDirectionsAndHalfClose(t *testing.T) {
	proxy := rawUpstream(t, func(conn net.Conn) {
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		r, err := http.ReadRequest(reader)
		if err != nil {
			t.Error(err)
			return
		}
		if r.Host != "ssh.example:22" || r.Header.Get("Proxy-Authorization") != "Bearer v1.egress" || r.Header.Get("X-ClawManager-Control-Token") != "" {
			t.Errorf("wrong protected CONNECT: host=%s", r.Host)
		}
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\nserver-hello")
		payload := make([]byte, 14)
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Error(err)
			return
		}
		conn.Write(payload)
		if _, err := io.Copy(io.Discard, reader); err != nil {
			t.Error(err)
			return
		}
		fmt.Fprint(conn, "after-eof")
	})
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) { t.Error("CONNECT reached backend API") })
	b := brokerForTest(t, backend.URL, "http://"+proxy.Addr().String())
	server := brokerServer(t, b)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(conn, "CONNECT ssh.example:22 HTTP/1.1\r\nHost: ssh.example:22\r\nProxy-Authorization: Bearer attacker\r\nX-ClawManager-Control-Token: evil\r\n\r\nclient-payload")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT response=%v err=%v", resp, err)
	}
	got := make([]byte, len("server-helloclient-payload"))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "server-helloclient-payload" {
		t.Fatalf("buffered bytes lost: %q %v", got, err)
	}
	conn.(*net.TCPConn).CloseWrite()
	got = make([]byte, len("after-eof"))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "after-eof" {
		t.Fatalf("half-close lost response: %q %v", got, err)
	}
}
func TestConnectRefusalNeverLeaksBodyAndBoundsHeaders(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			proxy := rawUpstream(t, func(conn net.Conn) {
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				http.ReadRequest(bufio.NewReader(conn))
				if oversized {
					fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nX-Secret: %s\r\n\r\n", strings.Repeat("secret-response", 3000))
				} else {
					fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 15\r\n\r\nsecret-response")
				}
			})
			backend := brokerBackend(t, func(http.ResponseWriter, *http.Request) {})
			b := brokerForTest(t, backend.URL, "http://"+proxy.Addr().String())
			rec := httptest.NewRecorder()
			b.Egress().ServeHTTP(rec, localRequest("CONNECT", "http://origin.invalid:22"))
			if rec.Code < 400 || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("upstream refusal leaked: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
func TestBrokerCancellationUnblocksPartialUploadAndActiveConnect(t *testing.T) {
	t.Run("upload", func(t *testing.T) {
		arrived := make(chan struct{})
		upDone := make(chan struct{})
		proxy := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
			defer close(upDone)
			io.CopyN(io.Discard, r.Body, 64<<10)
			close(arrived)
			io.Copy(io.Discard, r.Body)
		})
		backend := brokerBackend(t, func(http.ResponseWriter, *http.Request) {})
		b := brokerForTest(t, backend.URL, proxy.URL)
		server := brokerServer(t, b)
		client, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(client, "POST http://origin.invalid/upload HTTP/1.1\r\nHost: origin.invalid\r\nContent-Length: 1048576\r\n\r\n%s", strings.Repeat("x", 64<<10))
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("upload never arrived")
		}
		done := make(chan struct{})
		go func() { b.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("broker retained slow upload handler")
		}
		select {
		case <-upDone:
		case <-time.After(time.Second):
			t.Fatal("upstream upload retained")
		}
	})
	t.Run("connect", func(t *testing.T) {
		upDone := make(chan struct{})
		proxy := rawUpstream(t, func(conn net.Conn) {
			http.ReadRequest(bufio.NewReader(conn))
			fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n\r\n")
			io.Copy(io.Discard, conn)
			close(upDone)
		})
		backend := brokerBackend(t, func(http.ResponseWriter, *http.Request) {})
		b := brokerForTest(t, backend.URL, "http://"+proxy.Addr().String())
		server := brokerServer(t, b)
		client, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprint(client, "CONNECT origin.invalid:22 HTTP/1.1\r\nHost: origin.invalid:22\r\n\r\n")
		reader := bufio.NewReader(client)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil || resp.StatusCode != 200 {
			t.Fatal("CONNECT failed")
		}
		b.cfg.Credentials.(*Credentials).Close()
		select {
		case <-b.done:
		case <-time.After(time.Second):
			t.Fatal("credential loss did not join broker workers")
		}
		if _, err := reader.ReadByte(); err == nil {
			t.Fatal("canceled tunnel remained open")
		}
		select {
		case <-upDone:
		case <-time.After(time.Second):
			t.Fatal("upstream retained canceled tunnel")
		}
	})
}

func TestModelSSEFlushAndUpgrade(t *testing.T) {
	t.Run("sse", func(t *testing.T) {
		upDone := make(chan struct{})
		backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
			defer close(upDone)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		b := brokerForTest(t, backend.URL, backend.URL)
		server := httptest.NewServer(b.Model())
		defer func() { b.Close(); server.Close() }()
		resp, err := http.Get(server.URL + "/api/v1/gateway/llm/chat/completions")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		line := make(chan string, 1)
		go func() { reader := bufio.NewReader(resp.Body); s, _ := reader.ReadString('\n'); line <- s }()
		select {
		case s := <-line:
			if s != "data: first\n" {
				t.Fatalf("bad SSE event: %q", s)
			}
		case <-time.After(time.Second):
			t.Fatal("SSE buffered until EOF")
		}
		b.Close()
		select {
		case <-upDone:
		case <-time.After(time.Second):
			t.Fatal("SSE backend worker retained")
		}
	})
	t.Run("websocket-upgrade", func(t *testing.T) {
		backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Authorization") != "Bearer v1.model" {
				t.Error("upgrade/authentication lost")
			}
			conn, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nbackend-hello")
			data := make([]byte, len("client-hello"))
			if _, err := io.ReadFull(buffered, data); err != nil {
				t.Error(err)
				return
			}
			conn.Write(data)
		})
		b := brokerForTest(t, backend.URL, backend.URL)
		server := httptest.NewServer(b.Model())
		defer func() { b.Close(); server.Close() }()
		client, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(client, "GET /api/v1/gateway/llm/v1/responses HTTP/1.1\r\nHost: local\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nAuthorization: Bearer local-dummy\r\n\r\nclient-hello")
		reader := bufio.NewReader(client)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil || resp.StatusCode != 101 {
			t.Fatalf("upgrade response=%v err=%v", resp, err)
		}
		payload := make([]byte, len("backend-helloclient-hello"))
		if _, err := io.ReadFull(reader, payload); err != nil || string(payload) != "backend-helloclient-hello" {
			t.Fatalf("upgrade payload lost: %q %v", payload, err)
		}
	})
}

func TestSlowOriginAndModelMayDelayFirstResponseHeader(t *testing.T) {
	for _, model := range []bool{false, true} {
		t.Run(fmt.Sprint("model=", model), func(t *testing.T) {
			t.Parallel()
			backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(operationTimeout + 100*time.Millisecond):
					fmt.Fprint(w, "delayed-output")
				case <-r.Context().Done():
				}
			})
			b := brokerForTest(t, backend.URL, backend.URL)
			handler := b.Egress()
			target := "http://origin.invalid/slow"
			if model {
				handler = b.Model()
				target = "/api/v1/gateway/llm/chat/completions"
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, localRequest("GET", target))
			if rec.Code != 200 || rec.Body.String() != "delayed-output" {
				t.Fatalf("valid slow upstream failed: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
