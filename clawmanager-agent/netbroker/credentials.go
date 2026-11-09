// Package netbroker holds runtime capabilities in trusted management and relays
// one private network namespace's traffic only through fixed backend endpoints.
package netbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const operationTimeout = 5 * time.Second
const maxHeaders = 16 << 10

var errCredentials = errors.New("netbroker: credentials unavailable")

type Identity struct {
	InstanceID int    `json:"instance_id"`
	UserID     int    `json:"user_id"`
	Generation int    `json:"generation"`
	PodUID     string `json:"pod_uid"`
}
type Lease struct {
	Identity
	ModelToken    string    `json:"model_token"`
	EgressToken   string    `json:"egress_token"`
	ServicesToken string    `json:"services_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func validIdentity(id Identity) bool {
	if id.InstanceID <= 0 || id.UserID <= 0 || id.Generation <= 0 || len(id.PodUID) == 0 || len(id.PodUID) > 128 {
		return false
	}
	for _, c := range id.PodUID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func validToken(token string) bool {
	if len(token) > 4096 || !strings.HasPrefix(token, "v1.") || len(token) <= 3 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func fixedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") || !validHost(u.Hostname()) {
		return nil, errCredentials
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return nil, errCredentials
		}
	}
	u.Path = ""
	return u, nil
}
func directTransport() *http.Transport {
	return &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: operationTimeout, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: operationTimeout, ResponseHeaderTimeout: operationTimeout, MaxResponseHeaderBytes: maxHeaders, DisableCompression: true, DisableKeepAlives: true}
}

// Acquire sends the trusted control credential only to the fixed lease endpoint.
// It never follows redirects, uses environment proxies, or returns upstream text.
func Acquire(ctx context.Context, base, controlToken string, id Identity) (Lease, error) {
	u, err := fixedURL(base)
	if err != nil || !validIdentity(id) || controlToken == "" || len(controlToken) > 4096 || strings.ContainsAny(controlToken, "\r\n") {
		return Lease{}, errCredentials
	}
	u.Path = "/api/v1/runtime/security/lease"
	body, _ := json.Marshal(id)
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Lease{}, errCredentials
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ClawManager-Control-Token", controlToken)
	transport := directTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return Lease{}, errCredentials
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Lease{}, errCredentials
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxHeaders+1))
	if err != nil || len(b) > maxHeaders {
		return Lease{}, errCredentials
	}
	var lease Lease
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&lease) != nil {
		return Lease{}, errCredentials
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Lease{}, errCredentials
	}
	now := time.Now()
	if ctx.Err() != nil || lease.Identity != id || !lease.ExpiresAt.After(now) || lease.ExpiresAt.After(now.Add(15*time.Minute)) || !validToken(lease.ModelToken) || !validToken(lease.EgressToken) || !validToken(lease.ServicesToken) {
		return Lease{}, errCredentials
	}
	return lease, nil
}

type credentialTiming struct {
	renew time.Duration
	now   func() time.Time
}

// Credentials must not be copied. Close joins the refresh worker and clears tokens.
type Credentials struct {
	mu     sync.Mutex
	lease  Lease
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	timing credentialTiming
}

func NewCredentials(ctx context.Context, base, token string, id Identity) (*Credentials, error) {
	return newCredentials(ctx, base, token, id, credentialTiming{5 * time.Minute, time.Now})
}
func newCredentials(parent context.Context, base, token string, id Identity, timing credentialTiming) (*Credentials, error) {
	lease, err := Acquire(parent, base, token, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	c := &Credentials{lease: lease, ctx: ctx, cancel: cancel, done: make(chan struct{}), timing: timing}
	go func() {
		defer close(c.done)
		defer c.invalidate(errCredentials)
		ticker := time.NewTicker(timing.renew)
		defer ticker.Stop()
		expiry := time.NewTimer(lease.ExpiresAt.Sub(timing.now()))
		defer expiry.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-expiry.C:
				return
			case <-ticker.C:
				// A refresh may not extend an already expired management lifetime.
				refreshCtx, stop := context.WithDeadline(ctx, lease.ExpiresAt)
				next, err := Acquire(refreshCtx, base, token, id)
				stop()
				if err != nil || !timing.now().Before(lease.ExpiresAt) {
					return
				}
				c.mu.Lock()
				if ctx.Err() == nil {
					c.lease = next
				}
				c.mu.Unlock()
				lease = next
				if !expiry.Stop() {
					select {
					case <-expiry.C:
					default:
					}
				}
				expiry.Reset(lease.ExpiresAt.Sub(timing.now()))
			}
		}
	}()
	return c, nil
}
func (c *Credentials) invalidate(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.lease = Lease{}
	c.mu.Unlock()
	c.cancel()
}
func (c *Credentials) Snapshot() (Lease, error) {
	if c == nil {
		return Lease{}, errCredentials
	}
	c.mu.Lock()
	lease := c.lease
	available := c.err == nil && c.ctx.Err() == nil && c.timing.now().Before(lease.ExpiresAt)
	c.mu.Unlock()
	if !available {
		c.invalidate(errCredentials)
		return Lease{}, errCredentials
	}
	return lease, nil
}
func (c *Credentials) Context() context.Context { return c.ctx }
func (c *Credentials) Done() <-chan struct{}    { return c.done }
func (c *Credentials) Err() error               { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *Credentials) Close() error             { c.invalidate(errCredentials); <-c.done; return nil }
