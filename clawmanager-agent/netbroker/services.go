package netbroker

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

const servicesPrefix = "/api/v1/runtime/services"
const redisUpgradeProtocol = "clawmanager-redis"

func (b *Broker) Services() http.Handler { return http.HandlerFunc(b.services) }

func serviceRoute(method, p string) bool {
	if p == "/team/preview-sign" {
		return method == http.MethodPost
	}
	if p == "/json/new" {
		return method == http.MethodGet || method == http.MethodPut
	}
	if method != http.MethodGet {
		return false
	}
	if p == "/json" || p == "/json/version" || p == "/json/list" || p == "/json/protocol" {
		return true
	}
	return strings.HasPrefix(p, "/json/activate/") || strings.HasPrefix(p, "/json/close/") || strings.HasPrefix(p, "/devtools/")
}

func (b *Broker) services(w http.ResponseWriter, r *http.Request) {
	ctx, finish, ok := b.enter(w, r)
	if !ok {
		return
	}
	defer finish()
	p := r.URL.Path
	if r.URL.IsAbs() || r.URL.Host != "" || r.URL.Opaque != "" || r.URL.User != nil || r.URL.RawPath != "" || strings.ContainsAny(p, "\\\x00") || path.Clean(p) != p || !serviceRoute(r.Method, p) {
		reject(w, r, "invalid service route", 400)
		return
	}
	lease, err := b.cfg.Credentials.Snapshot()
	if err != nil {
		reject(w, r, "broker unavailable", 503)
		return
	}
	request := r.Clone(ctx)
	request.URL.Path = servicesPrefix + p
	if request.Header.Get("Upgrade") != "" {
		if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") || !strings.HasPrefix(p, "/devtools/") {
			reject(w, r, "unsupported upgrade", 400)
			return
		}
		b.modelUpgrade(w, request, ctx, lease.ServicesToken)
		return
	}
	// Reuse the fixed backend transport and managed-header filter, supplying
	// only the services audience. Neither model nor control authority is added.
	lease.ModelToken = lease.ServicesToken
	b.forward(w, request, ctx, lease, true)
}

// ServeTeamRedis exposes one named raw RESP service to the exact business child.
// It never interprets a business-selected URL or gives Redis credentials to it.
// The backend owns ACL provisioning, authentication and command-context guards.
func (b *Broker) ServeTeamRedis(parent context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	stopLifetime := context.AfterFunc(b.ctx, cancel)
	stopListener := joinedAfter(ctx, func() { listener.Close() })
	var workers sync.WaitGroup
	defer func() { cancel(); listener.Close(); workers.Wait(); stopListener(); stopLifetime() }()
	for {
		client, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || b.ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !samePeer(client.RemoteAddr().String(), b.child) {
			client.Close()
			continue
		}
		b.mu.Lock()
		admitted := !b.closing && b.ctx.Err() == nil && ctx.Err() == nil
		if admitted {
			select {
			case b.limit <- struct{}{}:
			default:
				admitted = false
			}
		}
		if !admitted {
			b.mu.Unlock()
			client.Close()
			continue
		}
		b.workers.Add(1)
		workers.Add(1)
		b.mu.Unlock()
		go func() {
			defer b.workers.Done()
			defer workers.Done()
			defer func() { <-b.limit }()
			defer client.Close()
			stopClient := joinedAfter(ctx, func() { client.Close() })
			defer stopClient()
			lease, err := b.cfg.Credentials.Snapshot()
			if err != nil {
				return
			}
			upstream, reader, err := b.upstreamRedis(ctx, lease.ServicesToken)
			if err != nil {
				return
			}
			defer upstream.Close()
			bridge(ctx, client, upstream, client, reader)
		}()
	}
}

func (b *Broker) upstreamRedis(ctx context.Context, token string) (net.Conn, *bufio.Reader, error) {
	port := b.backend.Port()
	if port == "" {
		port = "80"
		if b.backend.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := (&net.Dialer{Timeout: operationTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(b.backend.Hostname(), port))
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
	if conn.SetDeadline(time.Now().Add(operationTimeout)) != nil {
		return nil, nil, errBroker
	}
	if b.backend.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: b.backend.Hostname(), MinVersion: tls.VersionTLS12})
		if tlsConn.HandshakeContext(ctx) != nil {
			return nil, nil, errBroker
		}
		conn = tlsConn
	}
	if _, err := fmt.Fprintf(conn, "POST %s/team/redis HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\nContent-Length: 0\r\n\r\n", servicesPrefix, b.backend.Host, token, redisUpgradeProtocol); err != nil {
		return nil, nil, errBroker
	}
	limited := &boundedHeader{reader: conn, remaining: maxHeaders}
	reader := bufio.NewReader(limited)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil || response.StatusCode != 101 || !strings.EqualFold(response.Header.Get("Upgrade"), redisUpgradeProtocol) || !headerToken(response.Header, "Connection", "Upgrade") {
		return nil, nil, errBroker
	}
	limited.unlimited = true
	if conn.SetDeadline(time.Time{}) != nil || ctx.Err() != nil {
		return nil, nil, errBroker
	}
	ok = true
	return conn, reader, nil
}

func headerToken(header http.Header, name, value string) bool {
	for _, line := range header.Values(name) {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return true
			}
		}
	}
	return false
}
