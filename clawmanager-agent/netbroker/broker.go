package netbroker

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errBroker = errors.New("netbroker: upstream unavailable")
var errPolicyDenied = errors.New("netbroker: egress forbidden")

const modelPrefix = "/api/v1/gateway/llm"

type Config struct {
	ChildIP, BackendURL, EgressProxyURL string
	Credentials                         CredentialSource
}

// CredentialSource is held by trusted management, never by business code.
type CredentialSource interface {
	Snapshot() (Lease, error)
	Context() context.Context
}

// Broker owns request/tunnel workers, but starts no listener. The caller owns
// HTTP servers and their header/read limits. Config is copied and immutable.
type Broker struct {
	cfg                             Config
	child                           netip.Addr
	backend, proxy                  *url.URL
	ctx                             context.Context
	cancel                          context.CancelFunc
	done                            chan struct{}
	mu                              sync.Mutex
	closing                         bool
	workers                         sync.WaitGroup
	limit                           chan struct{}
	modelTransport, egressTransport *http.Transport
}

func New(parent context.Context, cfg Config) (*Broker, error) {
	child, err := netip.ParseAddr(cfg.ChildIP)
	if err != nil || child.Zone() != "" || child.IsUnspecified() || child.IsMulticast() || cfg.Credentials == nil {
		return nil, errBroker
	}
	backend, err := fixedURL(cfg.BackendURL)
	if err != nil {
		return nil, errBroker
	}
	proxy, err := fixedURL(cfg.EgressProxyURL)
	if err != nil {
		return nil, errBroker
	}
	if _, err := cfg.Credentials.Snapshot(); err != nil {
		return nil, errBroker
	}
	ctx, cancel := context.WithCancel(parent)
	b := &Broker{cfg: cfg, child: child.Unmap(), backend: backend, proxy: proxy, ctx: ctx, cancel: cancel, done: make(chan struct{}), limit: make(chan struct{}, 256), modelTransport: directTransport(), egressTransport: directTransport()}
	// Origin/model computation may delay response headers; lease/control and
	// CONNECT handshakes retain the shorter five-second bound.
	b.modelTransport.ResponseHeaderTimeout = time.Minute
	b.egressTransport.ResponseHeaderTimeout = time.Minute
	b.egressTransport.Proxy = http.ProxyURL(proxy)
	stop := context.AfterFunc(cfg.Credentials.Context(), cancel)
	go func() {
		defer close(b.done)
		defer stop()
		<-ctx.Done()
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()
		b.workers.Wait()
		b.modelTransport.CloseIdleConnections()
		b.egressTransport.CloseIdleConnections()
	}()
	return b, nil
}
func (b *Broker) Close() error         { b.cancel(); <-b.done; return nil }
func (b *Broker) Egress() http.Handler { return http.HandlerFunc(b.egress) }
func (b *Broker) Model() http.Handler  { return http.HandlerFunc(b.model) }
func samePeer(remote string, child netip.Addr) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Zone() == "" && ip.Unmap() == child
}

// joinedAfter prevents callbacks from touching a response writer after reuse.
func joinedAfter(ctx context.Context, fn func()) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); fn() })
	return func() {
		if !stop() {
			<-done
		}
	}
}
func (b *Broker) enter(w http.ResponseWriter, r *http.Request) (context.Context, func(), bool) {
	if !samePeer(r.RemoteAddr, b.child) {
		reject(w, r, "forbidden", 403)
		return nil, nil, false
	}
	b.mu.Lock()
	if b.closing || b.ctx.Err() != nil {
		b.mu.Unlock()
		reject(w, r, "broker unavailable", 503)
		return nil, nil, false
	}
	select {
	case b.limit <- struct{}{}:
	default:
		b.mu.Unlock()
		reject(w, r, "broker busy", 503)
		return nil, nil, false
	}
	b.workers.Add(1)
	b.mu.Unlock()
	parent := r.Context()
	if r.Method == http.MethodConnect {
		// net/http can cancel the request context on a TCP half-close, even
		// after Hijack. The broker lifetime owns CONNECT after admission.
		parent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	stopLifetime := context.AfterFunc(b.ctx, cancel)
	controller := http.NewResponseController(w)
	stopIO := joinedAfter(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); _ = controller.SetReadDeadline(time.Now()) })
	return ctx, func() { stopLifetime(); stopIO(); cancel(); <-b.limit; b.workers.Done() }, true
}
func stripHeaders(h http.Header, preserveWebsocket bool) {
	websocket := preserveWebsocket && strings.EqualFold(h.Get("Upgrade"), "websocket")
	for _, line := range h.Values("Connection") {
		for _, name := range strings.Split(line, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for key := range h {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-clawmanager-") || strings.HasPrefix(lower, "x-forwarded-") {
			h.Del(key)
		}
	}
	for _, name := range []string{"Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Connection", "Keep-Alive", "Forwarded", "X-Real-IP", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(name)
	}
	if websocket {
		h.Set("Upgrade", "websocket")
		h.Set("Connection", "Upgrade")
	}
}

const rejectionCleanupTimeout = 100 * time.Millisecond

// A handler return can leave net/http draining a small unread body outside our
// worker accounting. Reject while the deadline is active, close the body here,
// and retain the deadline/Connection: close through net/http's final cleanup.
// Forwarded uploads never use this short timeout.
func reject(w http.ResponseWriter, r *http.Request, message string, status int) {
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(rejectionCleanupTimeout))
	if r.Body != nil {
		_ = r.Body.Close()
	}
	http.Error(w, message, status)
}

func proxyError(w http.ResponseWriter, r *http.Request, _ error) {
	reject(w, r, "upstream unavailable", 502)
}
func (b *Broker) forward(w http.ResponseWriter, r *http.Request, ctx context.Context, lease Lease, model bool) {
	transport := b.egressTransport
	if model {
		transport = b.modelTransport
	}
	proxy := httputil.ReverseProxy{Transport: transport, FlushInterval: -1, ErrorHandler: proxyError,
		Rewrite: func(p *httputil.ProxyRequest) {
			stripHeaders(p.Out.Header, model)
			if model {
				p.Out.URL.Scheme = b.backend.Scheme
				p.Out.URL.Host = b.backend.Host
				p.Out.Host = b.backend.Host
				p.Out.Header.Del("X-Api-Key")
				p.Out.Header.Del("X-Goog-Api-Key")
				p.Out.Header.Set("Authorization", "Bearer "+lease.ModelToken)
			} else {
				p.Out.Header.Set("Proxy-Authorization", "Bearer "+lease.EgressToken)
			}
		}, ModifyResponse: func(response *http.Response) error {
			stripHeaders(response.Header, model)
			response.Header.Del("Authorization")
			return nil
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
func (b *Broker) model(w http.ResponseWriter, r *http.Request) {
	ctx, finish, ok := b.enter(w, r)
	if !ok {
		return
	}
	defer finish()
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		reject(w, r, "method not allowed", 405)
		return
	}
	p := r.URL.Path
	if r.URL.IsAbs() || r.URL.Host != "" || r.URL.User != nil || r.URL.RawPath != "" || strings.ContainsAny(p, "\\\x00") || path.Clean(p) != p || (p != modelPrefix && !strings.HasPrefix(p, modelPrefix+"/")) {
		reject(w, r, "invalid model route", 400)
		return
	}
	lease, err := b.cfg.Credentials.Snapshot()
	if err != nil {
		reject(w, r, "broker unavailable", 503)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			reject(w, r, "unsupported upgrade", 400)
			return
		}
		b.modelUpgrade(w, r, ctx, lease.ModelToken)
		return
	}
	b.forward(w, r, ctx, lease, true)
}
func validTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil || !validHost(host) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port
}
func (b *Broker) egress(w http.ResponseWriter, r *http.Request) {
	ctx, finish, ok := b.enter(w, r)
	if !ok {
		return
	}
	defer finish()
	lease, err := b.cfg.Credentials.Snapshot()
	if err != nil {
		reject(w, r, "broker unavailable", 503)
		return
	}
	if r.Method == http.MethodConnect {
		if !validTarget(r.Host) {
			reject(w, r, "invalid CONNECT target", 400)
			return
		}
		b.connect(w, r, ctx, lease.EgressToken)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Opaque != "" || r.URL.User != nil || !validHost(r.URL.Hostname()) || r.URL.Host == "" {
		reject(w, r, "absolute HTTP target required", 400)
		return
	}
	if p := r.URL.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			reject(w, r, "invalid HTTP target", 400)
			return
		}
	}
	b.forward(w, r, ctx, lease, false)
}

// boundedHeader switches to unlimited reads after parsing, retaining bytes that
// bufio already read beyond the CONNECT headers.
type boundedHeader struct {
	reader    io.Reader
	remaining int
	unlimited bool
}

func (h *boundedHeader) Read(p []byte) (int, error) {
	if !h.unlimited {
		if h.remaining <= 0 {
			return 0, errBroker
		}
		if len(p) > h.remaining {
			p = p[:h.remaining]
		}
	}
	n, err := h.reader.Read(p)
	h.remaining -= n
	return n, err
}
func (b *Broker) upstreamConnect(ctx context.Context, target, token string) (net.Conn, *bufio.Reader, error) {
	port := b.proxy.Port()
	if port == "" {
		port = "80"
		if b.proxy.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := (&net.Dialer{Timeout: operationTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(b.proxy.Hostname(), port))
	if err != nil {
		return nil, nil, errBroker
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	raw := conn
	stop := joinedAfter(ctx, func() { raw.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
		return nil, nil, errBroker
	}
	if b.proxy.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: b.proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if tlsConn.HandshakeContext(ctx) != nil {
			return nil, nil, errBroker
		}
		conn = tlsConn
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", target, target, token); err != nil {
		return nil, nil, errBroker
	}
	limited := &boundedHeader{reader: conn, remaining: maxHeaders}
	reader := bufio.NewReader(limited)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err == nil && response.StatusCode == http.StatusForbidden {
		return nil, nil, errPolicyDenied
	}
	if err != nil || response.StatusCode != 200 {
		return nil, nil, errBroker
	}
	limited.unlimited = true
	if conn.SetDeadline(time.Time{}) != nil || ctx.Err() != nil {
		return nil, nil, errBroker
	}
	ok = true
	return conn, reader, nil
}
func (b *Broker) connect(w http.ResponseWriter, r *http.Request, ctx context.Context, token string) {
	upstream, reader, err := b.upstreamConnect(ctx, r.Host, token)
	if err != nil {
		if errors.Is(err, errPolicyDenied) {
			reject(w, r, "egress forbidden", http.StatusForbidden)
			return
		}
		proxyError(w, r, err)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		proxyError(w, r, errBroker)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	stop := joinedAfter(ctx, func() { client.Close(); upstream.Close() })
	defer stop()
	client.SetDeadline(time.Now().Add(operationTimeout))
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if client.SetDeadline(time.Time{}) != nil {
		return
	}
	bridge(ctx, client, upstream, buffered.Reader, reader)
}
func bridge(ctx context.Context, client, upstream net.Conn, fromClient, fromUpstream io.Reader) {
	stop := joinedAfter(ctx, func() { client.Close(); upstream.Close() })
	defer stop()
	copyDirection := func(dst, src net.Conn, reader io.Reader) {
		if _, err := io.Copy(dst, reader); err != nil {
			client.Close()
			upstream.Close()
		} else if half, ok := dst.(interface{ CloseWrite() error }); ok {
			half.CloseWrite()
		} else {
			dst.Close()
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); copyDirection(upstream, client, fromClient) }()
	copyDirection(client, upstream, fromUpstream)
	<-done
}

// Handle upgrades explicitly: the installed ReverseProxy drops the client's
// hijacked buffered reader, and may return before both copy workers finish.
func (b *Broker) modelUpgrade(w http.ResponseWriter, r *http.Request, ctx context.Context, token string) {
	request := r.Clone(ctx)
	request.RequestURI = ""
	request.URL.Scheme, request.URL.Host, request.Host = b.backend.Scheme, b.backend.Host, b.backend.Host
	stripHeaders(request.Header, true)
	request.Header.Del("X-Api-Key")
	request.Header.Del("X-Goog-Api-Key")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := b.modelTransport.RoundTrip(request)
	if err != nil {
		proxyError(w, r, errBroker)
		return
	}
	defer response.Body.Close()
	backend, ok := response.Body.(io.ReadWriteCloser)
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || !ok {
		proxyError(w, r, errBroker)
		return
	}
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	stop := joinedAfter(ctx, func() { client.Close(); backend.Close() })
	defer stop()
	stripHeaders(response.Header, true)
	response.Header.Del("Authorization")
	response.Body = nil
	client.SetDeadline(time.Now().Add(operationTimeout))
	if response.Write(buffered) != nil || buffered.Flush() != nil {
		return
	}
	if client.SetDeadline(time.Time{}) != nil {
		return
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(backend, buffered.Reader); client.Close(); backend.Close() }()
	_, _ = io.Copy(client, backend)
	client.Close()
	backend.Close()
	<-done
}
