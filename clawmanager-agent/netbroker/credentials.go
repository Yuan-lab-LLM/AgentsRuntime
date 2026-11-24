// Package netbroker holds runtime capabilities in trusted management and relays
// one private network namespace's traffic only through fixed backend endpoints.
package netbroker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
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
const initialRetryDelay = 500 * time.Millisecond
const maxHeaders = 16 << 10

var errCredentials = errors.New("netbroker: credentials unavailable")
var errLeaseTemporary = fmt.Errorf("%w: lease temporarily unavailable", errCredentials)
var errLeaseRejected = fmt.Errorf("%w: lease rejected", errCredentials)
var errLeaseExpired = fmt.Errorf("%w: lease expired", errCredentials)

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
		// A certificate/redirect/configuration error is not a transient outage.
		var certificate *tls.CertificateVerificationError
		var network net.Error
		var operation *net.OpError
		if !errors.As(err, &certificate) && (ctx.Err() != nil || errors.As(err, &operation) || (errors.As(err, &network) && network.Timeout()) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			return Lease{}, errLeaseTemporary
		}
		return Lease{}, errCredentials
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case 429, 500, 502, 503, 504:
			return Lease{}, errLeaseTemporary
		default:
			return Lease{}, errLeaseRejected
		}
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxHeaders+1))
	if err != nil {
		return Lease{}, errLeaseTemporary
	}
	if len(b) > maxHeaders {
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
	renew    time.Duration
	now      func() time.Time
	retryMin time.Duration
	retryMax time.Duration
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
	return newCredentials(ctx, base, token, id, credentialTiming{renew: 5 * time.Minute, now: time.Now})
}
func newCredentials(parent context.Context, base, token string, id Identity, timing credentialTiming) (*Credentials, error) {
	lease, err := Acquire(parent, base, token, id)
	// A just-published assignment may meet a short backend outage. Retry only a
	// transient control-plane failure, before any workload or broker is started.
	if errors.Is(err, errLeaseTemporary) && parent.Err() == nil {
		timer := time.NewTimer(initialRetryDelay)
		defer timer.Stop()
		select {
		case <-parent.Done():
			return nil, err
		case <-timer.C:
			lease, err = Acquire(parent, base, token, id)
		}
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	c := &Credentials{lease: lease, ctx: ctx, cancel: cancel, done: make(chan struct{}), timing: timing}
	go func() {
		defer close(c.done)
		terminal := errCredentials
		defer func() { c.invalidate(terminal) }()
		nextRefresh := time.NewTimer(timing.renew)
		defer nextRefresh.Stop()
		expiry := time.NewTimer(lease.ExpiresAt.Sub(timing.now()))
		defer expiry.Stop()
		retryMin, retryMax := timing.retryMin, timing.retryMax
		if retryMin <= 0 {
			retryMin = time.Second
		}
		if retryMax < retryMin {
			retryMax = 30 * time.Second
		}
		backoff := retryMin
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-expiry.C:
				terminal = errLeaseExpired
				return
			case <-nextRefresh.C:
				// A refresh may not extend an already expired management lifetime.
				refreshCtx, stop := context.WithDeadline(ctx, lease.ExpiresAt)
				next, err := Acquire(refreshCtx, base, token, id)
				stop()
				if !timing.now().Before(lease.ExpiresAt) {
					terminal = errLeaseExpired
					return
				}
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if !errors.Is(err, errLeaseTemporary) {
						terminal = err
						log.Printf("runtime lease rejected: instance_id=%d generation=%d", id.InstanceID, id.Generation)
						return
					}
					failures++
					// No business request is replayed. The signed lease's original
					// expiry remains the hard deadline for renewal retries.
					delay := backoff/2 + time.Duration(rand.Int64N(int64(backoff-backoff/2)+1))
					remaining := lease.ExpiresAt.Sub(timing.now())
					if delay > remaining {
						delay = remaining
					}
					if failures == 1 {
						log.Printf("runtime lease retrying: instance_id=%d generation=%d remaining_seconds=%d", id.InstanceID, id.Generation, int64(remaining/time.Second))
					}
					nextRefresh.Reset(delay)
					if backoff < retryMax/2 {
						backoff *= 2
					} else {
						backoff = retryMax
					}
					continue
				}
				c.mu.Lock()
				if ctx.Err() == nil {
					c.lease = next
				}
				c.mu.Unlock()
				lease = next
				if failures > 0 {
					log.Printf("runtime lease recovered: instance_id=%d generation=%d failed_attempts=%d", id.InstanceID, id.Generation, failures)
				}
				failures = 0
				backoff = retryMin
				nextRefresh.Reset(timing.renew)
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
