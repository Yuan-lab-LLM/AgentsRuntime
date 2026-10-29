package hermes

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const testDashboardOrigin = "https://clawmanager.internal:9443"

type dashboardFixture struct {
	t                  *testing.T
	mu                 sync.Mutex
	logins             int
	websockets         int
	paths              []string
	healthStatus       int
	healthBody         string
	healthType         string
	authStatus         int
	authBody           string
	wsStatus           int
	badAccept          bool
	badPong            bool
	expireIdentityOnce bool
	identityBody       string
	identityStatus     int
	ticketBody         string
	ticketStatus       int
	wsProtocol         string
}

func (f *dashboardFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.RequestURI())
	if r.Header.Get("Origin") != testDashboardOrigin {
		f.t.Errorf("missing trusted Origin on %s", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Forwarded-Prefix") != "" {
		f.t.Errorf("unexpected authorization or forwarding header on direct probe")
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/health":
		if f.healthType != "" {
			w.Header().Set("Content-Type", f.healthType)
		}
		if f.healthStatus != 0 {
			w.WriteHeader(f.healthStatus)
		}
		if f.healthBody != "" {
			io.WriteString(w, f.healthBody)
		} else {
			io.WriteString(w, `{"ok":true,"version":"0.21.0","auth_required":true}`)
		}
	case "/auth/password-login":
		f.logins++
		if r.Method != http.MethodPost {
			f.t.Errorf("login method = %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["username"] != "clawmanager" || body["password"] != "private-password" || body["provider"] != "basic" {
			f.t.Error("login payload does not match the pinned password provider contract")
		}
		if f.authStatus != 0 {
			w.WriteHeader(f.authStatus)
			io.WriteString(w, f.authBody)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "hermes_session_at", Value: "private-cookie", Path: "/", HttpOnly: true})
		io.WriteString(w, `{"ok":true,"next":"/"}`)
	case "/api/auth/me":
		if !f.requireCookie(w, r) {
			return
		}
		if f.expireIdentityOnce {
			f.expireIdentityOnce = false
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.identityStatus != 0 {
			w.WriteHeader(f.identityStatus)
			return
		}
		if f.identityBody != "" {
			io.WriteString(w, f.identityBody)
			return
		}
		fmt.Fprintf(w, `{"user_id":"clawmanager","provider":"basic","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
	case "/api/auth/ws-ticket":
		if !f.requireCookie(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			f.t.Errorf("ticket method = %s", r.Method)
		}
		if f.ticketStatus != 0 {
			w.WriteHeader(f.ticketStatus)
			return
		}
		if f.ticketBody != "" {
			io.WriteString(w, f.ticketBody)
			return
		}
		io.WriteString(w, `{"ticket":"private-ticket","ttl_seconds":30}`)
	case "/api/events":
		f.websockets++
		if r.URL.RawQuery != "channel=clawmanager-readiness-63-7" {
			f.t.Errorf("unexpected WS query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Sec-WebSocket-Protocol") != "hermes-gateway-v1,hermes-gateway-ticket.private-ticket" {
			f.t.Error("websocket ticket was not carried in the expected subprotocol")
		}
		if f.wsStatus != 0 {
			w.WriteHeader(f.wsStatus)
			io.WriteString(w, "private-cookie private-ticket private-password")
			return
		}
		f.serveWebSocket(w, r)
	default:
		f.t.Errorf("readiness touched unexpected route %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *dashboardFixture) requireCookie(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("hermes_session_at")
	if err != nil || cookie.Value != "private-cookie" {
		f.t.Error("authenticated probe did not carry its instance cookie")
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *dashboardFixture) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, stream, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Error(err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	value := base64.StdEncoding.EncodeToString(accept[:])
	if f.badAccept {
		value = "invalid"
	}
	protocol := f.wsProtocol
	if protocol == "" {
		protocol = dashboardWSProtocol
	}
	if protocol == "omit" {
		protocol = ""
	}
	fmt.Fprintf(stream, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n", value, protocol)
	stream.Flush()
	if f.badAccept || protocol != dashboardWSProtocol {
		return
	}
	opcode, payload, err := readClientControlFrame(stream.Reader)
	if err != nil || opcode != 0x9 {
		f.t.Errorf("readiness did not send masked websocket ping: opcode=%d error=%v", opcode, err)
		return
	}
	if f.badPong {
		payload = []byte("wrong-heartbeat")
	}
	stream.Write(append([]byte{0x8a, byte(len(payload))}, payload...))
	stream.Flush()
	if !f.badPong {
		opcode, _, err := readClientControlFrame(stream.Reader)
		if err != nil || opcode != 0x8 {
			f.t.Errorf("readiness did not close websocket: opcode=%d error=%v", opcode, err)
		}
	}
}

func readClientControlFrame(r *bufio.Reader) (byte, []byte, error) {
	var header [6]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	if header[1]&0x80 == 0 || header[1]&0x7f > 125 {
		return 0, nil, fmt.Errorf("invalid client control frame")
	}
	payload := make([]byte, int(header[1]&0x7f))
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= header[2+i%4]
	}
	return header[0] & 0xf, payload, nil
}

func newDashboardHealthFixture(t *testing.T, fixture *dashboardFixture) (*healthChecker, gateway.GatewayStartSpec) {
	t.Helper()
	fixture.t = t
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	_, rawPort, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	workspace := t.TempDir()
	// Windows' TEMP may use an 8.3 alias. Production workspaces are canonical
	// paths; use the same shape here so short-name expansion is not a symlink.
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(workspace, "home", ".hermes")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", ".env"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("managed configuration\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	spec := gateway.GatewayStartSpec{
		InstanceID: 63, Generation: 7, Port: port, WorkspacePath: workspace,
		Env: []string{
			"CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED=true",
			"HERMES_HOME=" + home,
			"HERMES_DASHBOARD_BASIC_AUTH_USERNAME=clawmanager",
			"HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=private-password",
			"CLAWMANAGER_LLM_API_KEY=private-llm-key",
			"CLAWMANAGER_LLM_BASE_URL=http://clawmanager-backend.internal/v1",
		},
	}
	checker := newHealthChecker(gateway.Config{PublicOrigin: testDashboardOrigin, GatewayStartupTimeout: 3 * time.Second}).(*healthChecker)
	return checker, spec
}

func assertDashboardError(t *testing.T, err error, code string) {
	t.Helper()
	var typed *DashboardHealthError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want typed code %s", err, code)
	}
	for _, secret := range []string{"private-password", "private-cookie", "private-ticket", "private-llm-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Error("health error leaked private authentication material")
		}
	}
}

func TestDashboardHealthAuthenticatesHTTPAndWebSocketWithoutUserOperations(t *testing.T) {
	fixture := &dashboardFixture{}
	checker, spec := newDashboardHealthFixture(t, fixture)
	if err := checker.WaitReady(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.logins != 1 || fixture.websockets != 1 {
		t.Fatalf("login=%d websocket=%d, want 1 each", fixture.logins, fixture.websockets)
	}
	for _, path := range fixture.paths {
		if strings.Contains(path, "private-") || strings.HasPrefix(path, "/api/ws") {
			t.Fatalf("unsafe readiness URL %s", path)
		}
	}
}

func TestDashboardTeamHealthRequiresDashboardAndMatchingConsumer(t *testing.T) {
	fixture := &dashboardFixture{}
	checker, spec := newDashboardHealthFixture(t, fixture)
	checker.cfg.GatewayStartupTimeout = 180 * time.Millisecond
	readyFile := filepath.Join(spec.WorkspacePath, "home", ".clawmanager-team-worker", ".hermes", "runtime", "redis-team.ready.json")
	spec.Env = append(spec.Env,
		"CLAWMANAGER_TEAM_ENABLED=true",
		"CLAWMANAGER_TEAM_REDIS_URL=redis://redis.example.invalid:6379/0",
		"CLAWMANAGER_TEAM_ID=team-42",
		"CLAWMANAGER_TEAM_MEMBER_ID=developer",
		"CLAWMANAGER_TEAM_READY_FILE="+readyFile,
	)

	if err := checker.WaitReady(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "consumer readiness") {
		t.Fatalf("WaitReady() error = %v, want missing Team consumer readiness", err)
	}
	writeDashboardTeamStartupState(t, readyFile, map[string]any{
		"ready": true, "state": "ready", "runtime": "hermes",
		"teamId": "team-42", "memberId": "developer",
		"instanceId": 63, "generation": 7,
	})
	if err := checker.WaitReady(context.Background(), spec); err != nil {
		t.Fatalf("WaitReady() error = %v, want Dashboard and Team consumer ready", err)
	}
}

func writeDashboardTeamStartupState(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardHealthRejectsFalseHTTPReadiness(t *testing.T) {
	cases := []struct {
		name                    string
		status                  int
		body, contentType, code string
	}{
		{"login redirect", 302, `private-password private-cookie`, "text/html", "unsupported_hermes_protocol"},
		{"login HTML", 200, `<html>Login private-password</html>`, "text/html", "unsupported_hermes_protocol"},
		{"unauthorized", 401, `private-password`, "application/json", "dashboard_auth_failed"},
		{"wrong version", 200, `{"ok":true,"version":"0.16.0","auth_required":true}`, "application/json", "unsupported_hermes_protocol"},
		{"authentication disabled", 200, `{"ok":true,"version":"0.21.0","auth_required":false}`, "application/json", "dashboard_auth_failed"},
		{"malformed JSON", 200, `private-password`, "application/json", "unsupported_hermes_protocol"},
		{"oversized response", 200, strings.Repeat("x", dashboardMaxResponseBytes+1), "application/json", "unsupported_hermes_protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &dashboardFixture{healthStatus: tc.status, healthBody: tc.body, healthType: tc.contentType}
			checker, spec := newDashboardHealthFixture(t, fixture)
			assertDashboardError(t, checker.CheckHealth(context.Background(), spec), tc.code)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.logins != 0 || fixture.websockets != 0 {
				t.Error("probe continued after rejected HTTP health")
			}
		})
	}
}

func TestDashboardHealthRejectsCredentialFailureWithoutLeakingOrRetrying(t *testing.T) {
	fixture := &dashboardFixture{authStatus: 401, authBody: `{"error":"private-password private-cookie private-ticket"}`}
	checker, spec := newDashboardHealthFixture(t, fixture)
	assertDashboardError(t, checker.WaitReady(context.Background(), spec), "dashboard_auth_failed")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.logins != 1 {
		t.Fatalf("failed login retried %d times", fixture.logins)
	}
}

func TestDashboardHealthRenewsExpiredSession(t *testing.T) {
	fixture := &dashboardFixture{expireIdentityOnce: true}
	checker, spec := newDashboardHealthFixture(t, fixture)
	if err := checker.CheckHealth(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.logins != 2 {
		t.Fatalf("login count = %d, want session recovery", fixture.logins)
	}
}

func TestDashboardHealthRequiresWebSocketUpgradeAndHeartbeat(t *testing.T) {
	cases := []struct {
		name    string
		fixture dashboardFixture
		code    string
	}{
		{"origin or auth rejected", dashboardFixture{wsStatus: 403}, "websocket_upgrade_rejected"},
		{"no upgrade", dashboardFixture{wsStatus: 200}, "unsupported_hermes_protocol"},
		{"invalid accept", dashboardFixture{badAccept: true}, "unsupported_hermes_protocol"},
		{"wrong heartbeat", dashboardFixture{badPong: true}, "unsupported_hermes_protocol"},
		{"missing selected protocol", dashboardFixture{wsProtocol: "omit"}, "unsupported_hermes_protocol"},
		{"wrong selected protocol", dashboardFixture{wsProtocol: "hermes-jsonrpc-v1"}, "unsupported_hermes_protocol"},
		{"credential protocol echoed", dashboardFixture{wsProtocol: "hermes-gateway-ticket.private-ticket"}, "unsupported_hermes_protocol"},
	}
	for i := range cases {
		tc := &cases[i]
		t.Run(tc.name, func(t *testing.T) {
			checker, spec := newDashboardHealthFixture(t, &tc.fixture)
			assertDashboardError(t, checker.CheckHealth(context.Background(), spec), tc.code)
		})
	}
}

func TestDashboardHealthRejectsExpiredIdentityAndInvalidTickets(t *testing.T) {
	cases := []struct {
		name    string
		fixture dashboardFixture
		code    string
	}{
		{"revoked identity", dashboardFixture{identityStatus: 401}, "dashboard_auth_failed"},
		{"expired identity", dashboardFixture{identityBody: `{"user_id":"clawmanager","provider":"basic","expires_at":1}`}, "dashboard_auth_failed"},
		{"wrong identity", dashboardFixture{identityBody: `{"user_id":"other-instance","provider":"basic","expires_at":9999999999}`}, "dashboard_auth_failed"},
		{"revoked ticket authentication", dashboardFixture{ticketStatus: 401}, "dashboard_auth_failed"},
		{"ticket missing", dashboardFixture{ticketBody: `{"ttl_seconds":30}`}, "unsupported_hermes_protocol"},
		{"ticket expired", dashboardFixture{ticketBody: `{"ticket":"private-ticket","ttl_seconds":0}`}, "unsupported_hermes_protocol"},
		{"unsafe ticket", dashboardFixture{ticketBody: `{"ticket":"private-ticket?token=secret","ttl_seconds":30}`}, "unsupported_hermes_protocol"},
	}
	for i := range cases {
		tc := &cases[i]
		t.Run(tc.name, func(t *testing.T) {
			f := &tc.fixture
			checker, spec := newDashboardHealthFixture(t, f)
			assertDashboardError(t, checker.WaitReady(context.Background(), spec), tc.code)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.websockets != 0 || f.logins > 2 {
				t.Fatal("readiness continued or repeatedly reauthenticated after failure")
			}
			for _, path := range f.paths {
				if strings.Contains(path, "/api/ws") || strings.Contains(path, "/api/pty") || strings.Contains(path, "sessions") {
					t.Fatal("readiness used a stateful endpoint")
				}
			}
		})
	}
}

func TestDashboardHealthValidatesCredentialsWorkspaceAndOriginBeforeNetwork(t *testing.T) {
	cases := []struct{ name, key, value, code string }{
		{"missing LLM key", "CLAWMANAGER_LLM_API_KEY", "", "missing_llm_credentials"},
		{"missing LLM URL", "CLAWMANAGER_LLM_BASE_URL", "", "missing_llm_credentials"},
		{"missing password", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD", "", "dashboard_auth_failed"},
		{"foreign home", "HERMES_HOME", "/other-instance/home/.hermes", "workspace_unusable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &dashboardFixture{}
			checker, spec := newDashboardHealthFixture(t, fixture)
			spec.Env = setEnv(spec.Env, tc.key, tc.value)
			assertDashboardError(t, checker.CheckHealth(context.Background(), spec), tc.code)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.paths) != 0 {
				t.Fatal("invalid configuration reached network")
			}
		})
	}
	for _, origin := range []string{"", "*", "https://example.com/path", "https://example.com?private-password", "https://user:private-password@example.com"} {
		checker, spec := newDashboardHealthFixture(t, &dashboardFixture{})
		checker.cfg.PublicOrigin = origin
		assertDashboardError(t, checker.CheckHealth(context.Background(), spec), "invalid_control_ui_origin")
	}
}

func TestDashboardHealthDoesNotFollowLoginRedirect(t *testing.T) {
	var redirects int
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects++
		t.Error("credentials crossed a redirect boundary")
	}))
	defer external.Close()
	checker, spec := newDashboardHealthFixture(t, &dashboardFixture{})
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", external.URL+"/?private-password")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(redirect.URL, "http://"))
	spec.Port, _ = strconv.Atoi(port)
	assertDashboardError(t, checker.CheckHealth(context.Background(), spec), "unsupported_hermes_protocol")
	if redirects != 0 {
		t.Error("redirect was followed")
	}
}

func TestDashboardHealthStartupHonorsCancellationAndDeadline(t *testing.T) {
	checker, spec := newDashboardHealthFixture(t, &dashboardFixture{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer stalled.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(stalled.URL, "http://"))
	spec.Port, _ = strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := checker.WaitReady(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("probe cancellation took %s", elapsed)
	}
}
