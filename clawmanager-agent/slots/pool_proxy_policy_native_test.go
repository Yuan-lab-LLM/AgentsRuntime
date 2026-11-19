package slots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/control"
	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type nativePolicyFixture struct {
	cfg         gateway.Config
	pool        *Pool
	files       []string
	peers       []http.Handler
	unavailable map[int]bool
	mutateAck   func(*gateway.ProxyPolicy)
	conflict    bool
	starts      []int
}

func newNativePolicyFixture(t *testing.T) *nativePolicyFixture {
	t.Helper()
	f := &nativePolicyFixture{cfg: gateway.Config{ControlToken: "secret", Namespace: "tenant", RuntimeType: "hermes", PodUID: "pod-1", SandboxProfile: "nfs-rootless-v1", ProxyPolicyFile: filepath.Join(t.TempDir(), "coordinator", "policy.json"), GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}, unavailable: map[int]bool{}}
	for i := 0; i < 2; i++ {
		cfg := f.cfg
		cfg.ProxyPolicyFile = filepath.Join(t.TempDir(), "worker", "policy.json")
		f.files = append(f.files, cfg.ProxyPolicyFile)
		f.peers = append(f.peers, control.NewControlHandler(cfg, gateway.NewGatewayManager(cfg, nil, nil), nil))
	}
	var err error
	f.pool, err = NewPool(f.cfg, 2, func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if f.unavailable[index] {
			return 0, nil, errors.New("worker in kubelet backoff")
		}
		if path == "/v1/gateways" {
			// The real native process checks this file immediately before starting.
			raw, err := os.ReadFile(f.files[index])
			var policy gateway.ProxyPolicy
			if err != nil || json.Unmarshal(raw, &policy) != nil || policy.ExpiresAtMS <= time.Now().UnixMilli() {
				return 503, nil, nil
			}
			f.starts = append(f.starts, index)
			return 200, []byte(`{"gateway_id":"gw-7-1"}`), nil
		}
		if path != "/v1/proxy-policy" {
			t.Fatalf("unexpected worker path %s", path)
		}
		if f.conflict && method == http.MethodPut {
			return 409, nil, nil
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(body))).WithContext(ctx)
		r.Header.Set(gateway.ControlTokenHeader, f.cfg.ControlToken)
		w := httptest.NewRecorder()
		f.peers[index].ServeHTTP(w, r)
		data := w.Body.Bytes()
		if method == http.MethodPut && w.Code == 200 && f.mutateAck != nil {
			var policy gateway.ProxyPolicy
			if err := json.Unmarshal(data, &policy); err != nil {
				t.Fatal(err)
			}
			f.mutateAck(&policy)
			data, _ = json.Marshal(policy)
		}
		return w.Code, data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nativePolicyFixture) request(method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set(gateway.ControlTokenHeader, f.cfg.ControlToken)
	w := httptest.NewRecorder()
	f.pool.ServeHTTP(w, r)
	return w
}

func (f *nativePolicyFixture) update(expected string, expiry int64) *httptest.ResponseRecorder {
	return f.request(http.MethodPut, "/v1/proxy-policy", map[string]any{"namespace": f.cfg.Namespace, "expected_revision": expected, "proxies": []string{"10.1.2.3"}, "expires_at_ms": expiry})
}

func (f *nativePolicyFixture) create() *httptest.ResponseRecorder {
	return f.request(http.MethodPost, "/v1/gateways", gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 1, AgentType: f.cfg.RuntimeType, PodUID: f.cfg.PodUID, GatewayPort: 20001})
}

func TestNativePoolOwnsProxyPolicyWithoutWorkers(t *testing.T) {
	f := newNativePolicyFixture(t)
	f.unavailable[0], f.unavailable[1] = true, true
	w := f.request(http.MethodGet, "/v1/proxy-policy", nil)
	if w.Code != 200 {
		t.Fatalf("coordinator GET depended on slot0: %d %s", w.Code, w.Body.String())
	}
	w = f.update("", 0)
	if w.Code != 200 {
		t.Fatalf("canonical PUT depended on workers: %d %s", w.Code, w.Body.String())
	}
	var ack, stored, observed gateway.ProxyPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.cfg.ProxyPolicyFile)
	if err != nil || json.Unmarshal(raw, &stored) != nil || !reflect.DeepEqual(ack, stored) {
		t.Fatalf("canonical policy not persisted: %+v %v", stored, err)
	}
	w = f.request(http.MethodGet, "/v1/proxy-policy", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &observed) != nil || !reflect.DeepEqual(ack, observed) {
		t.Fatalf("canonical GET mismatch: %d %s", w.Code, w.Body.String())
	}
	if w = f.update("", 0); w.Code != 409 {
		t.Fatalf("stale canonical CAS accepted: %d %s", w.Code, w.Body.String())
	}
	if w = f.create(); w.Code != 503 || len(f.starts) != 0 {
		t.Fatal("unavailable selected worker started")
	}
}

func TestNativePoolResynchronizesSelectedWorkerWithoutExtendingLease(t *testing.T) {
	f := newNativePolicyFixture(t)
	expiry := time.Now().Add(20 * time.Second).UnixMilli()
	if w := f.update("", expiry); w.Code != 200 {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	f.unavailable[0] = true
	// A replacement worker has no policy or previous revision. Only the selected
	// worker must receive the canonical policy before the native start is forwarded.
	cfg := f.cfg
	cfg.ProxyPolicyFile = f.files[1]
	if err := os.Remove(f.files[1]); err != nil {
		t.Fatal(err)
	}
	f.peers[1] = control.NewControlHandler(cfg, gateway.NewGatewayManager(cfg, nil, nil), nil)
	if w := f.create(); w.Code != 200 || !reflect.DeepEqual(f.starts, []int{1}) {
		t.Fatalf("healthy sibling was not resynchronized: status=%d starts=%v body=%s", w.Code, f.starts, w.Body.String())
	}
	raw, err := os.ReadFile(f.files[1])
	var worker gateway.ProxyPolicy
	if err != nil || json.Unmarshal(raw, &worker) != nil || worker.ExpiresAtMS != expiry {
		t.Fatalf("worker extended canonical lease: %+v err=%v want=%d", worker, err, expiry)
	}
}

func TestNativePoolRejectsExpiredConflictingOrMismatchedSelectedPolicy(t *testing.T) {
	for _, scenario := range []string{"missing", "expired", "cas-conflict", "wrong-expiry", "wrong-address", "wrong-namespace"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNativePolicyFixture(t)
			if scenario != "missing" {
				expiry := int64(0)
				if scenario == "expired" {
					expiry = time.Now().Add(time.Second).UnixMilli()
				}
				if w := f.update("", expiry); w.Code != 200 {
					t.Fatalf("seed: %d %s", w.Code, w.Body.String())
				}
				if scenario == "expired" {
					time.Sleep(time.Until(time.UnixMilli(expiry + 10)))
				}
			}
			switch scenario {
			case "cas-conflict":
				f.conflict = true
			case "wrong-expiry":
				f.mutateAck = func(p *gateway.ProxyPolicy) { p.ExpiresAtMS++ }
			case "wrong-address":
				f.mutateAck = func(p *gateway.ProxyPolicy) { p.Proxies = []string{"10.9.9.9"} }
			case "wrong-namespace":
				f.mutateAck = func(p *gateway.ProxyPolicy) { p.Namespace = "other" }
			}
			w := f.create()
			if w.Code != 503 || len(f.starts) != 0 || !strings.Contains(w.Body.String(), "PROXY_POLICY_NOT_READY") {
				t.Fatalf("unsafe selected start: status=%d starts=%v body=%s", w.Code, f.starts, w.Body.String())
			}
		})
	}
}

func TestNativePoolHungSiblingDoesNotBlockCanonicalReadOrHealthyStart(t *testing.T) {
	f := newNativePolicyFixture(t)
	original := f.pool.call
	waiting := make(chan struct{}, 1)
	f.pool.call = func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if index == 0 {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
				t.Error("worker refresh is not bounded")
			}
			select {
			case waiting <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return 0, nil, ctx.Err()
		}
		return original(ctx, index, method, path, body)
	}
	updated := make(chan *httptest.ResponseRecorder, 1)
	go func() { updated <- f.update("", 0) }()
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("fanout never reached sibling")
	}
	// GET must use the already-persisted canonical store even during a refresh.
	if w := f.request(http.MethodGet, "/v1/proxy-policy", nil); w.Code != 200 {
		t.Fatalf("canonical GET blocked: %d", w.Code)
	}
	started := make(chan *httptest.ResponseRecorder, 1)
	go func() { started <- f.create() }()
	select {
	case w := <-updated:
		if w.Code != 200 {
			t.Fatalf("hung sibling rejected canonical PUT: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hung sibling blocked canonical PUT")
	}
	select {
	case w := <-started:
		if w.Code != 200 {
			t.Fatalf("hung sibling blocked healthy start: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("healthy start did not resume after bounded refresh")
	}
}

func TestNativePoolSerializesSelectedPolicyApplyAndPermitsRenewalDuringCreate(t *testing.T) {
	f := newNativePolicyFixture(t)
	w := f.update("", 0)
	if w.Code != 200 {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	var first gateway.ProxyPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	original := f.pool.call
	selectedRead, allowRead := make(chan struct{}), make(chan struct{})
	creating, allowCreate := make(chan struct{}), make(chan struct{})
	var blockRead atomic.Bool
	blockRead.Store(true)
	var readRelease, createRelease sync.Once
	defer readRelease.Do(func() { close(allowRead) })
	defer createRelease.Do(func() { close(allowCreate) })
	f.pool.call = func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if index == 1 && path == "/v1/proxy-policy" && method == http.MethodGet && blockRead.CompareAndSwap(true, false) {
			close(selectedRead)
			select {
			case <-allowRead:
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}
		if path == "/v1/gateways" {
			close(creating)
			select {
			case <-allowCreate:
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}
		return original(ctx, index, method, path, body)
	}
	created := make(chan *httptest.ResponseRecorder, 1)
	go func() { created <- f.create() }()
	select {
	case <-selectedRead:
	case <-time.After(2 * time.Second):
		t.Fatal("selected worker sync missing")
	}
	updated := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		updated <- f.request(http.MethodPut, "/v1/proxy-policy", gateway.ProxyPolicyUpdate{Namespace: f.cfg.Namespace, ExpectedRevision: first.Revision, Proxies: []string{"10.1.2.4"}})
	}()
	select {
	case <-updated:
		t.Fatal("canonical update raced selected policy application")
	case <-time.After(50 * time.Millisecond):
	}
	readRelease.Do(func() { close(allowRead) })
	select {
	case <-creating:
	case <-time.After(2 * time.Second):
		t.Fatal("create was not forwarded after sync")
	}
	// A slow native process start must not hold the policy mutex or prevent TTL
	// renewal. The old selected policy cannot be applied after this newer one.
	select {
	case w := <-updated:
		if w.Code != 200 {
			t.Fatalf("renewal failed: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long create prevented policy renewal")
	}
	for _, file := range f.files {
		raw, err := os.ReadFile(file)
		var policy gateway.ProxyPolicy
		if err != nil || json.Unmarshal(raw, &policy) != nil || !reflect.DeepEqual(policy.Proxies, []string{"10.1.2.4"}) {
			t.Fatalf("older selected policy overwrote newer renewal: %+v %v", policy, err)
		}
	}
	createRelease.Do(func() { close(allowCreate) })
	if w := <-created; w.Code != 200 {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
}

func TestNativePoolWithoutProxyPolicyDoesNotQueryWorkers(t *testing.T) {
	f := newNativePolicyFixture(t)
	f.cfg.ProxyPolicyFile = ""
	calls := 0
	var err error
	f.pool, err = NewPool(f.cfg, 2, func(context.Context, int, string, string, []byte) (int, []byte, error) {
		calls++
		return 0, nil, errors.New("worker in backoff")
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := f.request(method, "/v1/proxy-policy", gateway.ProxyPolicyUpdate{Namespace: f.cfg.Namespace, Proxies: []string{"10.1.2.3"}})
		if w.Code != 404 || calls != 0 {
			t.Fatalf("unsupported policy queried worker: method=%s status=%d calls=%d body=%s", method, w.Code, calls, w.Body.String())
		}
	}
}
