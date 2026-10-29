package slots

import (
	"context"
	"crypto/subtle"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const nativeIngressHeader = "X-ClawManager-Ingress-Token"

// One immutable Unix destination per published port. No URL/header can select a
// different native port. The capability here grants only this native UI.
type nativeIngress struct {
	ctx       context.Context
	cancel    context.CancelFunc
	token     string
	transport *http.Transport
	mu        sync.Mutex
	closing   bool
	active    map[io.Closer]bool
	workers   sync.WaitGroup
	limit     chan struct{}
}

func newNativeIngress(ctx context.Context, token string, dial func(context.Context) (net.Conn, error)) *nativeIngress {
	ctx, cancel := context.WithCancel(ctx)
	p := &nativeIngress{ctx: ctx, cancel: cancel, token: token, active: map[io.Closer]bool{}, limit: make(chan struct{}, 256)}
	p.transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }, MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16}
	context.AfterFunc(ctx, p.shutdown)
	return p
}
func (p *nativeIngress) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closing = true
	for c := range p.active {
		c.Close()
	}
	p.transport.CloseIdleConnections()
}
func (p *nativeIngress) Close() error { p.cancel(); p.shutdown(); p.workers.Wait(); return nil }
func rejectNativeIngress(w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if r.Body != nil {
		r.Body.Close()
	}
	http.Error(w, http.StatusText(status), status)
}
func (p *nativeIngress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	local := err == nil && ip != nil && ip.IsLoopback()
	tokens := r.Header.Values(nativeIngressHeader)
	if !local && (len(tokens) != 1 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(p.token)) != 1) {
		rejectNativeIngress(w, r, 401)
		return
	}
	p.mu.Lock()
	if p.closing || p.ctx.Err() != nil {
		p.mu.Unlock()
		rejectNativeIngress(w, r, 503)
		return
	}
	select {
	case p.limit <- struct{}{}:
	default:
		p.mu.Unlock()
		rejectNativeIngress(w, r, 503)
		return
	}
	p.workers.Add(1)
	p.mu.Unlock()
	defer func() { <-p.limit; p.workers.Done() }()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	ioStopped := make(chan struct{})
	stopIO := context.AfterFunc(ctx, func() {
		defer close(ioStopped)
		c := http.NewResponseController(w)
		c.SetReadDeadline(time.Now())
		c.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stopIO() {
			<-ioStopped
		}
	}()
	request := r.Clone(ctx)
	request.RequestURI = ""
	request.URL.Scheme = "http"
	request.URL.Host = "native"
	request.URL.Opaque = ""
	request.Header = r.Header.Clone()
	request.Header.Del(nativeIngressHeader)
	upgrade := strings.EqualFold(request.Header.Get("Upgrade"), "websocket") && headerToken(request.Header.Get("Connection"), "upgrade")
	stripNativeHopHeaders(request.Header, upgrade)
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		rejectNativeIngress(w, r, 502)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 101 {
		if !upgrade || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || !headerToken(response.Header.Get("Connection"), "upgrade") {
			rejectNativeIngress(w, r, 502)
			return
		}
		upstream, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			rejectNativeIngress(w, r, 502)
			return
		}
		incoming, buffer, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closing {
			p.mu.Unlock()
			incoming.Close()
			return
		}
		p.active[incoming] = true
		p.active[upstream] = true
		p.mu.Unlock()
		defer func() {
			incoming.Close()
			upstream.Close()
			p.mu.Lock()
			delete(p.active, incoming)
			delete(p.active, upstream)
			p.mu.Unlock()
		}()
		stripNativeHopHeaders(response.Header, true)
		if _, err = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
			return
		}
		if err = response.Header.Write(buffer); err != nil {
			return
		}
		if _, err = buffer.WriteString("\r\n"); err != nil {
			return
		}
		if err = buffer.Flush(); err != nil {
			return
		}
		copied := make(chan struct{}, 2)
		go func() { io.Copy(upstream, buffer.Reader); copied <- struct{}{} }()
		go func() { io.Copy(incoming, upstream); copied <- struct{}{} }()
		completed := 0
		select {
		case <-copied:
			completed = 1
		case <-ctx.Done():
		}
		incoming.Close()
		upstream.Close()
		for completed < 2 {
			<-copied
			completed++
		}
		return
	}
	stripNativeHopHeaders(response.Header, false)
	for k, values := range response.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(response.StatusCode)
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		buffer := make([]byte, 32<<10)
		for {
			n, e := response.Body.Read(buffer)
			if n > 0 {
				if _, err = w.Write(buffer[:n]); err != nil {
					return
				}
				if err = http.NewResponseController(w).Flush(); err != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}
	io.Copy(w, response.Body)
}
func headerToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
func stripNativeHopHeaders(h http.Header, upgrade bool) {
	for _, value := range h.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			key = strings.TrimSpace(key)
			if !upgrade || !strings.EqualFold(key, "Upgrade") {
				h.Del(key)
			}
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding"} {
		h.Del(key)
	}
	h.Del(nativeIngressHeader)
	if upgrade {
		h.Set("Connection", "Upgrade")
	} else {
		h.Del("Upgrade")
	}
}
