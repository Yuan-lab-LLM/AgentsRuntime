package hermes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

const (
	dashboardProbeTimeout     = 5 * time.Second
	dashboardProbeInterval    = 500 * time.Millisecond
	dashboardMaxResponseBytes = 64 << 10
	dashboardPackageVersion   = "0.21.0"
	dashboardWSProtocol       = "hermes-gateway-v1"
)

// DashboardHealthError deliberately never wraps transport errors, response
// bodies, URLs, cookies, or credentials: it is safe for gateway reports/logs.
type DashboardHealthError struct {
	Code    string
	Message string
}

func (e *DashboardHealthError) Error() string { return e.Code + ": " + e.Message }

func dashboardError(code, message string) error {
	return &DashboardHealthError{Code: code, Message: message}
}

type dashboardProbe struct {
	client        *http.Client
	baseURL       string
	origin        string
	username      string
	password      string
	cookies       []*http.Cookie
	loginAttempts int
}

func (h *healthChecker) waitDashboardReady(ctx context.Context, spec gateway.GatewayStartSpec) error {
	timeout := h.cfg.GatewayStartupTimeout
	if timeout <= 0 {
		timeout = defaultHermesStartupTimeout
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe, err := newDashboardProbe(h.cfg, spec)
	if err != nil {
		return err
	}
	defer probe.client.CloseIdleConnections()
	var lastErr error
	for {
		if readyCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if lastErr != nil {
				return lastErr
			}
			return dashboardError("dashboard_not_ready", "startup deadline exceeded")
		}
		lastErr = probe.check(readyCtx, spec)
		if lastErr == nil {
			return nil
		}
		// Rejected credentials and an unsupported protocol cannot heal while
		// starting the same process. Avoid triggering upstream login throttling.
		if e, ok := lastErr.(*DashboardHealthError); ok &&
			(e.Code == "dashboard_auth_failed" || e.Code == "unsupported_hermes_protocol") {
			return lastErr
		}
		timer := time.NewTimer(dashboardProbeInterval)
		select {
		case <-readyCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// CheckHealth performs one bounded probe for ongoing lifecycle supervision.
// All authentication state is scoped to this instance/generation and call.
func (h *healthChecker) CheckHealth(ctx context.Context, spec gateway.GatewayStartSpec) error {
	if !truthy(envValue(spec.Env, "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED")) {
		return h.http.CheckHealth(ctx, spec)
	}
	probe, err := newDashboardProbe(h.cfg, spec)
	if err != nil {
		return err
	}
	defer probe.client.CloseIdleConnections()
	return probe.check(ctx, spec)
}

func newDashboardProbe(cfg gateway.Config, spec gateway.GatewayStartSpec) (*dashboardProbe, error) {
	if spec.Port < 1 || spec.Port > 65535 || spec.InstanceID < 1 || spec.Generation < 1 {
		return nil, dashboardError("invalid_gateway_identity", "managed instance, generation, and port are required")
	}
	origin := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_CONTROL_UI_ORIGIN"))
	if origin == "" {
		origin = strings.TrimSpace(cfg.PublicOrigin)
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || strings.ContainsAny(origin, "*\r\n") {
		return nil, dashboardError("invalid_control_ui_origin", "an explicit trusted HTTP origin is required")
	}
	username := envValue(spec.Env, "HERMES_DASHBOARD_BASIC_AUTH_USERNAME")
	password := envValue(spec.Env, "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD")
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, dashboardError("dashboard_auth_failed", "managed dashboard username and password are required")
	}
	if strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_LLM_API_KEY")) == "" ||
		strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_LLM_BASE_URL")) == "" {
		return nil, dashboardError("missing_llm_credentials", "instance LLM credentials and gateway URL are required")
	}
	if err := dashboardWorkspaceReadable(spec); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		// Internal probes must never inherit HTTP_PROXY / HTTPS_PROXY.
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: time.Second}).DialContext,
		MaxResponseHeaderBytes: 16 << 10,
		ResponseHeaderTimeout:  2 * time.Second,
	}
	return &dashboardProbe{
		client: &http.Client{Transport: transport, Timeout: dashboardProbeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL:  fmt.Sprintf("http://127.0.0.1:%d", spec.Port),
		origin:   origin,
		username: username, password: password,
	}, nil
}

func dashboardWorkspaceReadable(spec gateway.GatewayStartSpec) error {
	root, err := workspaceio.Open(spec.WorkspacePath)
	if err != nil {
		return dashboardError("workspace_unusable", "workspace cannot be opened")
	}
	defer root.Close()
	expected := filepath.Join(spec.WorkspacePath, "home", ".hermes")
	if !filepath.IsAbs(spec.WorkspacePath) || filepath.Clean(envValue(spec.Env, "HERMES_HOME")) != filepath.Clean(expected) {
		return dashboardError("workspace_unusable", "Hermes home does not match the managed instance")
	}
	for index, path := range []string{spec.WorkspacePath, expected, filepath.Join(expected, "config.yaml"), filepath.Join(expected, ".env")} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || filepath.Clean(resolved) != filepath.Clean(path) {
			return dashboardError("workspace_unusable", "managed configuration is missing or contains a symbolic link")
		}
		file, err := root.Open(path)
		if err != nil {
			return dashboardError("workspace_unusable", "managed configuration is not readable")
		}
		info, err := file.Stat()
		file.Close()
		if err != nil || (index < 2 && !info.IsDir()) || (index >= 2 && !info.Mode().IsRegular()) {
			return dashboardError("workspace_unusable", "managed configuration is not a regular file or directory")
		}
	}
	return nil
}

func (p *dashboardProbe) check(ctx context.Context, spec gateway.GatewayStartSpec) error {
	probeCtx, cancel := context.WithTimeout(ctx, dashboardProbeTimeout)
	defer cancel()
	var health struct {
		OK           bool   `json:"ok"`
		Version      string `json:"version"`
		AuthRequired bool   `json:"auth_required"`
	}
	if err := p.requestJSON(probeCtx, http.MethodGet, "/api/health", nil, &health, false); err != nil {
		return err
	}
	if !health.OK || health.Version != dashboardPackageVersion {
		return dashboardError("unsupported_hermes_protocol", "dashboard health response does not match the pinned release")
	}
	if !health.AuthRequired {
		return dashboardError("dashboard_auth_failed", "dashboard authentication is not enabled")
	}
	if len(p.cookies) == 0 {
		if err := p.login(probeCtx); err != nil {
			return err
		}
	}
	if err := p.checkIdentity(probeCtx); err != nil {
		if e, ok := err.(*DashboardHealthError); !ok || e.Code != "dashboard_auth_failed" {
			return err
		}
		// A process restart or an expired cookie requires a fresh supported login.
		p.cookies = nil
		if err := p.login(probeCtx); err != nil {
			return err
		}
		if err := p.checkIdentity(probeCtx); err != nil {
			return err
		}
	}
	var ticket struct {
		Ticket string `json:"ticket"`
		TTL    int    `json:"ttl_seconds"`
	}
	if err := p.requestJSON(probeCtx, http.MethodPost, "/api/auth/ws-ticket", nil, &ticket, true); err != nil {
		return err
	}
	if ticket.TTL <= 0 || !safeTicket(ticket.Ticket) {
		return dashboardError("unsupported_hermes_protocol", "dashboard websocket ticket response is invalid")
	}
	return p.probeWebSocket(probeCtx, spec, ticket.Ticket)
}

func (p *dashboardProbe) login(ctx context.Context) error {
	// The pinned password provider allows 10 attempts/minute. A failed
	// startup must not repeatedly guess credentials at the polling cadence.
	if p.loginAttempts >= 3 {
		return dashboardError("dashboard_auth_failed", "dashboard session recovery retry budget exhausted")
	}
	p.loginAttempts++
	body, _ := json.Marshal(map[string]string{"provider": "basic", "username": p.username, "password": p.password})
	var result struct {
		OK bool `json:"ok"`
	}
	if err := p.requestJSON(ctx, http.MethodPost, "/auth/password-login", body, &result, true); err != nil {
		return err
	}
	if !result.OK || !p.hasSessionCookie() {
		return dashboardError("dashboard_auth_failed", "dashboard login did not establish a session")
	}
	return nil
}

func (p *dashboardProbe) checkIdentity(ctx context.Context) error {
	var identity struct {
		UserID    string `json:"user_id"`
		Provider  string `json:"provider"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := p.requestJSON(ctx, http.MethodGet, "/api/auth/me", nil, &identity, true); err != nil {
		return err
	}
	if identity.UserID != p.username || identity.Provider != "basic" || identity.ExpiresAt <= time.Now().Unix() {
		return dashboardError("dashboard_auth_failed", "dashboard session identity is invalid or expired")
	}
	return nil
}

func (p *dashboardProbe) requestJSON(ctx context.Context, method, path string, body []byte, result any, authenticated bool) error {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return dashboardError("unsupported_hermes_protocol", "cannot construct dashboard probe")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", p.origin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		for _, cookie := range p.cookies {
			req.AddCookie(cookie)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return dashboardError("dashboard_http_unavailable", "dashboard HTTP probe did not complete")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return dashboardError("dashboard_auth_failed", "dashboard rejected the authenticated request")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return dashboardError("dashboard_auth_failed", "dashboard authentication is rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return dashboardError("dashboard_http_unavailable", "dashboard HTTP probe returned a server error")
		}
		return dashboardError("unsupported_hermes_protocol", "dashboard endpoint returned an unexpected status")
	}
	if contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || contentType != "application/json" {
		return dashboardError("unsupported_hermes_protocol", "dashboard endpoint did not return JSON")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, dashboardMaxResponseBytes+1))
	if err != nil || len(data) > dashboardMaxResponseBytes || json.Unmarshal(data, result) != nil {
		return dashboardError("unsupported_hermes_protocol", "dashboard JSON response is invalid or too large")
	}
	// Cookies are kept only within this probe and replayed only to this exact
	// internal instance. Never forward a server-supplied Domain elsewhere.
	for _, cookie := range resp.Cookies() {
		if cookie.Name != "hermes_session_at" && cookie.Name != "hermes_session_rt" && cookie.Name != "hermes_session_provider" {
			continue
		}
		if err := cookie.Valid(); err != nil {
			continue
		}
		found := false
		for i, old := range p.cookies {
			if old.Name == cookie.Name {
				p.cookies[i] = cookie
				found = true
				break
			}
		}
		if !found {
			p.cookies = append(p.cookies, cookie)
		}
	}
	return nil
}

func (p *dashboardProbe) hasSessionCookie() bool {
	for _, cookie := range p.cookies {
		if cookie.Name == "hermes_session_at" && cookie.Value != "" && cookie.MaxAge >= 0 {
			return true
		}
	}
	return false
}

func safeTicket(ticket string) bool {
	if len(ticket) == 0 || len(ticket) > 1024 {
		return false
	}
	for _, r := range ticket {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (p *dashboardProbe) probeWebSocket(ctx context.Context, spec gateway.GatewayStartSpec, ticket string) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", spec.Port))
	if err != nil {
		return dashboardError("websocket_unavailable", "dashboard websocket port is unavailable")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return dashboardError("websocket_unavailable", "cannot bound websocket probe")
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return dashboardError("websocket_unavailable", "cannot generate websocket handshake")
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	// /api/ws starts the upstream orphan-session sweep upon admission. The
	// events endpoint exercises the same authentication and Origin guard but
	// only creates an ephemeral in-memory subscription. No JSON-RPC, session,
	// model, tool, PTY, or filesystem operation is sent by this probe.
	path := fmt.Sprintf("/api/events?channel=clawmanager-readiness-%d-%d", spec.InstanceID, spec.Generation)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	req.Header.Set("Origin", p.origin)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Protocol", dashboardWSProtocol+",hermes-gateway-ticket."+ticket)
	if err := req.Write(conn); err != nil {
		return dashboardError("websocket_unavailable", "websocket handshake could not be sent")
	}
	reader := bufio.NewReader(io.LimitReader(conn, dashboardMaxResponseBytes))
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return dashboardError("websocket_unavailable", "websocket handshake did not complete")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return dashboardError("websocket_upgrade_rejected", "dashboard rejected websocket authentication or origin")
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || !headerHasToken(resp.Header, "Connection", "upgrade") ||
		!strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return dashboardError("unsupported_hermes_protocol", "dashboard did not perform a websocket upgrade")
	}
	accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	protocol := resp.Header.Get("Sec-WebSocket-Protocol")
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(accept[:]) ||
		protocol != dashboardWSProtocol || resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		return dashboardError("unsupported_hermes_protocol", "websocket handshake validation failed")
	}
	// Ping/pong is handled by the websocket transport without invoking the
	// events application's receive_text handler or creating user activity.
	if err := writeMaskedControlFrame(conn, 0x9, keyBytes); err != nil {
		return dashboardError("websocket_unavailable", "websocket ping failed")
	}
	for count := 0; count < 4; count++ {
		opcode, payload, err := readControlFrame(reader)
		if err != nil {
			return dashboardError("websocket_unavailable", "websocket heartbeat did not complete")
		}
		if opcode == 0xa && bytes.Equal(payload, keyBytes) {
			_ = writeMaskedControlFrame(conn, 0x8, []byte{0x03, 0xe8})
			return nil
		}
		if opcode == 0x9 {
			if err := writeMaskedControlFrame(conn, 0xa, payload); err != nil {
				break
			}
		} else {
			break
		}
	}
	return dashboardError("unsupported_hermes_protocol", "websocket did not acknowledge the readiness heartbeat")
}

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}

func writeMaskedControlFrame(w io.Writer, opcode byte, payload []byte) error {
	if len(payload) > 125 {
		return fmt.Errorf("control frame too large")
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	frame := append([]byte{0x80 | opcode, 0x80 | byte(len(payload))}, mask...)
	for i, value := range payload {
		frame = append(frame, value^mask[i%4])
	}
	_, err := w.Write(frame)
	return err
}

func readControlFrame(r io.Reader) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	if header[0]&0xf0 != 0x80 || header[1]&0x80 != 0 || header[1] > 125 {
		return 0, nil, fmt.Errorf("invalid websocket control frame")
	}
	opcode := header[0] & 0xf
	if opcode != 0x8 && opcode != 0x9 && opcode != 0xa {
		return 0, nil, fmt.Errorf("unexpected websocket frame")
	}
	payload := make([]byte, int(header[1]))
	_, err := io.ReadFull(r, payload)
	return opcode, payload, err
}
