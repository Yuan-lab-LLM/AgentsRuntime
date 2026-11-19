package netbroker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestManagedServicesFixedRoutesAndServicesAudience(t *testing.T) {
	seen := make(chan *http.Request, 2)
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		fmt.Fprint(w, "service-output")
	})
	b := brokerForTest(t, backend.URL, backend.URL)
	for _, route := range []string{"/team/preview-sign", "/json/version"} {
		method := "GET"
		if route == "/team/preview-sign" {
			method = "POST"
		}
		r := localRequest(method, route)
		r.Header.Set("Authorization", "Bearer forged-model")
		r.Header.Set("X-ClawManager-Control-Token", "control-sentinel")
		rec := httptest.NewRecorder()
		b.Services().ServeHTTP(rec, r)
		if rec.Code != 200 || rec.Body.String() != "service-output" {
			t.Fatalf("service status %d", rec.Code)
		}
		got := <-seen
		if got.URL.Path != "/api/v1/runtime/services"+route || got.Header.Get("Authorization") != "Bearer v1.services" || got.Header.Get("X-ClawManager-Control-Token") != "" {
			t.Fatal("service route/audience/header boundary failed")
		}
	}
	for _, route := range []string{"/api/v1/runtime/security/lease", "/team/redis", "/json/../admin", "/json/%2e%2e/admin", "http://evil.invalid/json/version"} {
		rec := httptest.NewRecorder()
		b.Services().ServeHTTP(rec, localRequest("GET", route))
		if rec.Code < 400 {
			t.Fatal("arbitrary service route accepted")
		}
	}
}

func TestTeamRedisFixedUpgradePreservesBufferedDataAndHalfClose(t *testing.T) {
	seen := make(chan *http.Request, 1)
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		c, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: clawmanager-redis\r\n\r\n+READY\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(c, buffered.Reader)
	})
	b := brokerForTest(t, backend.URL, backend.URL)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.ServeTeamRedis(ctx, l) }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	payload := "*1\r\n$4\r\nPING\r\n"
	_, _ = io.WriteString(c, payload)
	_ = c.(*net.TCPConn).CloseWrite()
	data, err := io.ReadAll(c)
	if err != nil || string(data) != "+READY\r\n"+payload {
		t.Fatalf("RESP bytes/half-close: %q %v", data, err)
	}
	request := <-seen
	if request.URL.Path != "/api/v1/runtime/services/team/redis" || request.Header.Get("Authorization") != "Bearer v1.services" || !strings.EqualFold(request.Header.Get("Upgrade"), "clawmanager-redis") {
		t.Fatal("arbitrary Redis target or audience")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Redis listener did not join")
	}
}

func TestTeamRedisRejectsWrongChildWithoutBackendDial(t *testing.T) {
	backend := brokerBackend(t, func(http.ResponseWriter, *http.Request) { t.Error("wrong peer dialed backend") })
	b := brokerForTest(t, backend.URL, backend.URL)
	b.child = netip.MustParseAddr("127.0.0.2")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.ServeTeamRedis(ctx, l) }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := bufio.NewReader(c).ReadByte(); err == nil {
		t.Fatal("wrong child received service")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wrong child listener did not stop")
	}
}

func TestManagedCDPUpgradePreservesBufferedTraffic(t *testing.T) {
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runtime/services/devtools/browser/own" || r.Header.Get("Authorization") != "Bearer v1.services" {
			t.Error("CDP route or audience lost")
		}
		c, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nworker-hello")
		data := make([]byte, len("client-hello"))
		if _, err := io.ReadFull(buffered.Reader, data); err != nil {
			return
		}
		_, _ = c.Write(data)
	})
	b := brokerForTest(t, backend.URL, backend.URL)
	server := httptest.NewServer(b.Services())
	defer server.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(c, "GET /devtools/browser/own HTTP/1.1\r\nHost: local\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nclient-hello")
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 101 {
		t.Fatal("CDP upgrade failed")
	}
	payload := make([]byte, len("worker-helloclient-hello"))
	if _, err := io.ReadFull(reader, payload); err != nil || string(payload) != "worker-helloclient-hello" {
		t.Fatal("buffered CDP traffic lost")
	}
}

func TestTeamRedisCredentialRevocationClosesBlockedConnection(t *testing.T) {
	backendDone := make(chan struct{})
	backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(backendDone)
		c, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: clawmanager-redis\r\n\r\n+READY\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(io.Discard, buffered.Reader)
	})
	b := brokerForTest(t, backend.URL, backend.URL)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.ServeTeamRedis(context.Background(), l) }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(c)
	if line, err := reader.ReadString('\n'); err != nil || line != "+READY\r\n" {
		t.Fatal("Redis bridge not ready")
	}
	_ = b.cfg.Credentials.(*Credentials).Close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("revoked bridge retained connection")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("revoked listener did not join")
	}
	select {
	case <-backendDone:
	case <-time.After(time.Second):
		t.Fatal("revoked upstream retained")
	}
}
