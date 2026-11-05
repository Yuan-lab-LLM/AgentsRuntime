package slots

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
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

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netrelay"
	"github.com/iamlovingit/clawmanager-agent/workbench"
)

type previewFixtureStarter struct{ sandboxStarter }

func (s previewFixtureStarter) PrepareGateway(context.Context, gateway.CreateGatewayRequest, int) (func(context.Context) error, error) {
	return nil, nil
}
func (s previewFixtureStarter) StartGateway(context.Context, gateway.GatewayStartSpec) (gateway.ManagedProcess, error) {
	return gateway.ManagedProcess{PID: 42, Stop: func(context.Context) error { return nil }, VerifyListeners: func(context.Context) error { return nil }}, nil
}

func previewFixture(t *testing.T) (*Worker, *httptest.Server, *networkSession) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cm-preview-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	socket := filepath.Join(dir, "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	transparent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- netrelay.RunWithPreview(ctx, netrelay.Config{Broker: "127.0.0.1:31280", Preview: true}, transparent, nil, listener)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	cfg := gateway.Config{ControlToken: "secret", PodUID: "pod", RuntimeType: "openclaw", WorkspaceRoot: dir, AgentDataDir: filepath.Join(dir, "data"), Capacity: 1, IsolatedLifecycle: true, GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3, NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 1, Generation: 2, PodUID: "pod", AgentType: "openclaw", UID: os.Getuid(), GID: os.Getgid(), GatewayPort: 20000, WorkspacePath: filepath.Join(dir, "openclaw", "user-1", "instance-7")}
	session := newTestNetworkSession(ctx, nil)
	session.req = req
	session.port = 20000
	session.previewSocket = socket
	owner := &networkOwner{active: session}
	starter := previewFixtureStarter{sandboxStarter{cfg: cfg, network: owner}}
	// The fixture supplies a pre-existing private session; production preparation
	// is separately exercised by the gated Linux namespace acceptance test.
	managerCfg := cfg
	managerCfg.NetworkSecurityProfile = ""
	manager := gateway.NewGatewayManager(managerCfg, starter, nil)
	worker := &Worker{cfg: cfg, manager: manager, network: owner, book: &book{Assignment: &Assignment{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: "gw-7-2", State: "starting"}}}
	if _, err = manager.CreateGateway(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, _ := manager.GatewayState("gw-7-2")
		if state.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not running %+v", state)
		}
		time.Sleep(time.Millisecond)
	}
	unix, err := net.Listen("unix", filepath.Join(dir, "worker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: worker}
	go server.Serve(unix)
	t.Cleanup(func() { server.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", unix.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	pool, err := NewPool(cfg, 1, func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		r, _ := http.NewRequestWithContext(ctx, method, "http://slot"+path, bytes.NewReader(body))
		r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
		resp, e := transport.RoundTrip(r)
		if e != nil {
			return 0, nil, e
		}
		defer resp.Body.Close()
		data, e := io.ReadAll(resp.Body)
		return resp.StatusCode, data, e
	}, func(int) http.RoundTripper { return transport })
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(pool)
	t.Cleanup(public.Close)
	return worker, public, session
}
func previewRequest(t *testing.T, base string, route workbench.PreviewRoute, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", base+workbench.PreviewPath, body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(route)
	r.Header.Set(workbench.PreviewHeader, base64.RawURLEncoding.EncodeToString(raw))
	r.Header.Set(gateway.ControlTokenHeader, "secret")
	return r
}
func appRoute(app *httptest.Server) workbench.PreviewRoute {
	return workbench.PreviewRoute{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: "gw-7-2", Scheme: "http", Host: "localhost", Port: app.Listener.Addr().(*net.TCPAddr).Port, Path: "/root/asset", RawPath: "/root%2Fasset", RawQuery: "q=a%2Fb"}
}
func TestPreviewPoolWorkerHelperHTTP(t *testing.T) {
	worker, pool, session := previewFixture(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/root%2Fasset?q=a%2Fb" || r.Header.Get("Authorization") != "Bearer application" || r.Header.Get("Cookie") != "app=value" || r.Header.Get("Origin") != "http://"+r.Host {
			t.Errorf("wrong application request: %s %#v", r.URL.RequestURI(), r.Header)
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-clawmanager-") {
				t.Errorf("credential leaked: %s", k)
			}
		}
		data, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(201)
		w.Write(data)
	}))
	defer app.Close()
	route := appRoute(app)
	payload := bytes.Repeat([]byte{0, 1, 255}, 1<<16)
	request := previewRequest(t, pool.URL, route, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer application")
	request.Header.Set("Cookie", "app=value")
	request.Header.Set("Origin", "http://capability.localhost")
	request.Header.Set("X-ClawManager-Other", "secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 || !bytes.Equal(body, payload) {
		t.Fatalf("status/body %d %d", response.StatusCode, len(body))
	}
	for _, kind := range []string{"generation", "gateway", "user", "instance", "native", "helper", "external", "auth", "stop"} {
		t.Run(kind, func(t *testing.T) {
			bad := route
			switch kind {
			case "generation":
				bad.Generation++
			case "gateway":
				bad.GatewayID = "other"
			case "user":
				bad.UserID++
			case "instance":
				bad.InstanceID++
			case "native":
				bad.Port = 20001
			case "helper":
				bad.Port = 15000
			case "external":
				bad.Host = "8.8.8.8"
			case "stop":
				worker.mu.Lock()
				worker.book.Assignment.IdleStop = true
				worker.mu.Unlock()
			}
			r := previewRequest(t, pool.URL, bad, nil)
			if kind == "auth" {
				r.Header.Del(gateway.ControlTokenHeader)
			}
			resp, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			defer resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = session.close(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewPoolWorkerHelperWebSocketCleanup(t *testing.T) {
	for _, mode := range []string{"network", "manager"} {
		t.Run(mode, func(t *testing.T) {
			worker, pool, session := previewFixture(t)
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, b, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				defer c.Close()
				fmt.Fprint(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
				b.Flush()
				io.Copy(c, b)
			}))
			defer app.Close()
			route := appRoute(app)
			r := previewRequest(t, pool.URL, route, nil)
			r.Method = "GET"
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			c, err := net.Dial("tcp", pool.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			r.Write(c)
			reader := bufio.NewReader(c)
			resp, err := http.ReadResponse(reader, r)
			if err != nil || resp.StatusCode != 101 {
				t.Fatalf("upgrade: %v %+v", err, resp)
			}
			frame := []byte{0x81, 2, 'o', 'k'}
			c.Write(frame)
			got := make([]byte, len(frame))
			if _, err = io.ReadFull(reader, got); err != nil || !bytes.Equal(frame, got) {
				t.Fatalf("echo %v %v", got, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var stopErr error
			if mode == "network" {
				stopErr = session.close(ctx)
			} else {
				stopErr = worker.manager.DeleteGateway(ctx, "gw-7-2")
			}
			if err = stopErr; err != nil {
				t.Fatal(err)
			}
			if _, err = reader.ReadByte(); err == nil {
				t.Fatal("upgrade survived network teardown")
			}
		})
	}
}

func TestPreviewSimultaneousPortsAndIPv6Fallback(t *testing.T) {
	_, pool, _ := previewFixture(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<script src="/asset.js"></script>`)
	}))
	defer first.Close()
	ipv6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable")
	}
	second := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ipv6") }))
	second.Listener = ipv6
	second.Start()
	defer second.Close()
	results := make(chan error, 3)
	for _, entry := range []struct {
		app        *httptest.Server
		host, want string
	}{{first, "127.0.0.1", `<script src="/asset.js"></script>`}, {second, "::1", "ipv6"}, {second, "localhost", "ipv6"}} {
		go func() {
			route := appRoute(entry.app)
			route.Host = entry.host
			r := previewRequest(t, pool.URL, route, nil)
			resp, e := http.DefaultClient.Do(r)
			if e != nil {
				results <- e
				return
			}
			defer resp.Body.Close()
			b, e := io.ReadAll(resp.Body)
			if e == nil && (resp.StatusCode != 200 || string(b) != entry.want) {
				e = fmt.Errorf("%s: %d %q", entry.host, resp.StatusCode, b)
			}
			results <- e
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

func TestPreviewSSEStreamsAndRequestCancellation(t *testing.T) {
	_, pool, session := previewFixture(t)
	ended := make(chan struct{})
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: ready\n\n")
		http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := previewRequest(t, pool.URL, appRoute(app), nil).WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make([]byte, len("data: ready\n\n"))
	if _, err = io.ReadFull(resp.Body, got); err != nil || string(got) != "data: ready\n\n" {
		t.Fatalf("SSE buffered: %q %v", got, err)
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("request cancel retained application stream")
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = session.close(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewCapabilitiesRequireLivePrivateSession(t *testing.T) {
	worker, _, session := previewFixture(t)
	req := workbench.Request{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: "gw-7-2", Operation: "capabilities"}
	result, err := worker.manager.Workbench(context.Background(), req)
	if err != nil || !result.(map[string]any)["preview"].(bool) {
		t.Fatalf("capabilities %+v %v", result, err)
	}
	session.cancel()
	result, err = worker.manager.Workbench(context.Background(), req)
	if err != nil || result.(map[string]any)["preview"].(bool) {
		t.Fatalf("stale capabilities %+v %v", result, err)
	}
}

func TestPreviewServerShutdownCancelsStreamsBeforeJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: ready\n\n")
			http.NewResponseController(w).Flush()
			<-r.Context().Done()
		}), func(stop context.Context) error { return stop.Err() }, time.Second)
	}()
	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for stream before cancelling its context")
	}
}

// Opt-in cross-repository smoke harness. It opens only test-owned ephemeral
// listeners, never a production debug endpoint, and always exits within 90s.
func TestWorkbenchPreviewExternalHarness(t *testing.T) {
	path := os.Getenv("CM_WORKBENCH_PREVIEW_HARNESS_FILE")
	if path == "" {
		t.Skip("external CM smoke only")
	}
	_, pool, _ := previewFixture(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			c, b, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			hash := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			fmt.Fprintf(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
			b.Flush()
			for {
				header := make([]byte, 2)
				if _, err = io.ReadFull(b, header); err != nil {
					return
				}
				size := int(header[1] & 127)
				if size > 125 || header[0]&15 == 8 {
					return
				}
				mask := make([]byte, 4)
				if header[1]&128 != 0 {
					if _, err = io.ReadFull(b, mask); err != nil {
						return
					}
				}
				data := make([]byte, size)
				if _, err = io.ReadFull(b, data); err != nil {
					return
				}
				if header[1]&128 != 0 {
					for i := range data {
						data[i] ^= mask[i%4]
					}
				}
				c.Write(append([]byte{header[0], byte(size)}, data...))
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "body", 400)
			return
		}
		w.Header().Set("X-Test-Request-URI", r.URL.RequestURI())
		w.Header().Set("X-Test-Authorization", r.Header.Get("Authorization"))
		w.Header().Set("X-Test-Cookie", r.Header.Get("Cookie"))
		w.Header().Set("X-Test-Control-Leaked", "false")
		for key := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-clawmanager-") {
				w.Header().Set("X-Test-Control-Leaked", "true")
				http.Error(w, "management header leaked", 500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(body)
	}))
	defer app.Close()
	metadata := map[string]any{"endpoint": pool.URL, "token": "secret", "instance_id": 7, "user_id": 1, "generation": 2, "gateway_id": "gw-7-2", "gateway_port": 20000, "application_port": app.Listener.Addr().(*net.TCPAddr).Port}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				os.Remove(path + ".stop")
				return
			}
		}
	}
}
