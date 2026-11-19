package slots

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netrelay"
	"github.com/iamlovingit/clawmanager-agent/workbench"
)

func (p *Pool) serveWorkbenchPreview(w http.ResponseWriter, r *http.Request) {
	route, err := workbench.DecodePreview(r)
	if err != nil {
		workbenchError(w, err)
		return
	}
	index, err := p.workbenchSlot(r.Context(), route.Scope())
	if err != nil {
		workbenchError(w, err)
		return
	}
	if p.stream == nil {
		workbenchError(w, workbench.Failure("unsupported_runtime", "Private preview transport is unavailable."))
		return
	}
	transport := p.stream(index)
	if transport == nil {
		workbenchError(w, workbench.Failure("unsupported_runtime", "Private preview transport is unavailable."))
		return
	}
	proxy := httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = "http"
		pr.Out.URL.Host = "slot"
		pr.Out.URL.Path = workbench.PreviewPath
		pr.Out.URL.RawPath = ""
		pr.Out.URL.RawQuery = ""
		pr.Out.Host = "slot"
		pr.Out.Header.Set(gateway.ControlTokenHeader, p.cfg.ControlToken)
	}, ErrorHandler: previewError}
	proxy.ServeHTTP(w, r)
}
func (w *Worker) serveWorkbenchPreview(out http.ResponseWriter, r *http.Request) {
	route, err := workbench.DecodePreview(r)
	if err != nil {
		workbenchError(out, err)
		return
	}
	req := route.Scope()
	w.mu.Lock()
	if w.book == nil || w.book.quarantined || w.book.StopIntent != nil || w.book.Maintenance != nil || !matchesWorkbench(w.book.Assignment, req) {
		w.mu.Unlock()
		workbenchError(out, gateway.ErrStaleGeneration)
		return
	}
	err = w.manager.WorkbenchPreviewWithAdmission(r.Context(), req, w.mu.Unlock, func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) error {
		transport := &http.Transport{DialContext: dial, DisableKeepAlives: true, MaxConnsPerHost: 1, ResponseHeaderTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10}
		defer transport.CloseIdleConnections()
		target := route.URL()
		proxy := httputil.ReverseProxy{Transport: transport, FlushInterval: -1, ErrorHandler: previewError, Rewrite: func(pr *httputil.ProxyRequest) {
			*pr.Out.URL = *target
			pr.Out.Host = target.Host
			for key := range pr.Out.Header {
				if strings.HasPrefix(strings.ToLower(key), "x-clawmanager-") {
					pr.Out.Header.Del(key)
				}
			}
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("Referer")
			if pr.Out.Header.Get("Origin") != "" {
				pr.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
		}}
		// Interrupt incoming request-body reads and downstream writes on teardown.
		// ReverseProxy joins its upgraded copy loops after the upstream socket closes.
		stopped := make(chan struct{})
		stopIO := context.AfterFunc(ctx, func() {
			defer close(stopped)
			c := http.NewResponseController(out)
			c.SetReadDeadline(time.Now())
			c.SetWriteDeadline(time.Now())
		})
		defer func() {
			if !stopIO() {
				<-stopped
			}
		}()
		proxy.ServeHTTP(out, r.WithContext(ctx))
		return nil
	})
	if err != nil {
		workbenchError(out, err)
	}
}
func previewError(w http.ResponseWriter, _ *http.Request, _ error) {
	http.Error(w, "Preview service unavailable.", http.StatusBadGateway)
}

func (s sandboxStarter) GatewayPreviewAvailable(spec gateway.GatewayStartSpec) bool {
	if s.cfg.NetworkSecurityProfile != "isolated-network-v1" {
		return false
	}
	session, err := s.network.lookup(spec)
	return err == nil && session.previewSocket != ""
}
func (s sandboxStarter) DialGatewayPreview(ctx context.Context, spec gateway.GatewayStartSpec, network, address string) (net.Conn, error) {
	if network != "tcp" || !s.GatewayPreviewAvailable(spec) {
		return nil, workbench.Failure("unsupported_runtime", "Private preview transport is unavailable.")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, workbench.Invalid()
	}
	targets := []string{address}
	if host == "localhost" {
		targets = []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port)}
	}
	native := make([]int, s.cfg.GatewayPortBlockSize)
	for i := range native {
		native[i] = spec.Port + i
	}
	for _, target := range targets {
		if !netrelay.PreviewTarget(target, native) {
			return nil, workbench.Invalid()
		}
	}
	session, finish, err := s.network.join(spec)
	if err != nil {
		return nil, err
	}
	joined, cancel := context.WithCancel(ctx)
	stopSession := context.AfterFunc(session.ctx, cancel)
	var conn net.Conn
	for _, target := range targets {
		conn, err = netrelay.DialPreview(joined, session.previewSocket, target)
		if err == nil {
			break
		}
	}
	if err != nil {
		stopSession()
		cancel()
		finish(nil)
		return nil, err
	}
	wrapped := &networkPreviewConn{Conn: conn}
	wrapped.finish = func() { stopSession(); cancel(); finish(nil) }
	stopConn := context.AfterFunc(joined, func() { wrapped.Close() })
	// A separate wrapper unregisters cancellation on normal HTTP/WS completion.
	return &previewCloseConn{Conn: wrapped, stop: stopConn}, nil
}

type networkPreviewConn struct {
	net.Conn
	once   sync.Once
	finish func()
}

func (c *networkPreviewConn) Close() error { err := c.Conn.Close(); c.once.Do(c.finish); return err }

type previewCloseConn struct {
	net.Conn
	stop func() bool
}

func (c *previewCloseConn) Close() error { c.stop(); return c.Conn.Close() }
