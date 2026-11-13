package dshproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Renew before dispatch rather than replaying a potentially mutating request
// after a 401. One refresh serves all concurrent browser requests.
type browserSession struct {
	mu        sync.Mutex
	launchURL *url.URL
	value     string
	refreshAt time.Time
}

func (s *browserSession) cookie(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value != "" && time.Now().Before(s.refreshAt) {
		return s.value, nil
	}
	_, cookie, err := exchangeBrowserSession(ctx, s.launchURL)
	if err != nil {
		return "", err
	}
	ttl := 5 * time.Minute
	if cookie.MaxAge > 0 {
		ttl = time.Duration(cookie.MaxAge) * time.Second
	} else if !cookie.Expires.IsZero() {
		ttl = time.Until(cookie.Expires)
	}
	// Half the advertised lifetime leaves room for clock drift and dispatch.
	s.refreshAt = time.Now().Add(ttl / 2)
	s.value = cookie.Name + "=" + cookie.Value
	return s.value, nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trustedDesktopRequest(request *http.Request) bool {
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !isLoopbackHost(remote) {
		return false
	}
	host, _, err := net.SplitHostPort(request.Host)
	if err != nil || !isLoopbackHost(host) {
		return false
	}
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := request.Header.Get("Origin")
	return origin == "" || origin == "http://"+request.Host
}
