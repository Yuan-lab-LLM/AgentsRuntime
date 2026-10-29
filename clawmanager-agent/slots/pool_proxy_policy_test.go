package slots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/control"
	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestPoolProxyPolicyUsesEachPeerRevision(t *testing.T) {
	for _, partialFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "consecutive_updates", true: "partial_failure_retry"}[partialFailure], func(t *testing.T) {
			cfg := gateway.Config{ControlToken: "secret", Namespace: "tenant", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 1}
			peers := make([]http.Handler, 3)
			puts := 0
			fail := false
			for i := range peers {
				peerCfg := cfg
				peerCfg.ProxyPolicyFile = filepath.Join(t.TempDir(), "policy", "policy.json")
				peers[i] = control.NewControlHandler(peerCfg, gateway.NewGatewayManager(peerCfg, nil, nil), nil)
			}
			call := func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
				if method == http.MethodPut {
					puts++
					if fail && index == 1 {
						return 0, nil, errors.New("temporary peer failure")
					}
				}
				r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
				r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
				w := httptest.NewRecorder()
				peers[index].ServeHTTP(w, r)
				return w.Code, w.Body.Bytes(), nil
			}
			pool, err := NewPool(cfg, len(peers), call)
			if err != nil {
				t.Fatal(err)
			}
			request := func(method string, update gateway.ProxyPolicyUpdate) *httptest.ResponseRecorder {
				body, _ := json.Marshal(update)
				r := httptest.NewRequest(method, "/v1/proxy-policy", strings.NewReader(string(body)))
				r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
				w := httptest.NewRecorder()
				pool.ServeHTTP(w, r)
				return w
			}
			current := func() gateway.ProxyPolicy {
				w := request(http.MethodGet, gateway.ProxyPolicyUpdate{})
				var policy gateway.ProxyPolicy
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &policy) != nil {
					t.Fatalf("GET: %d %s", w.Code, w.Body.String())
				}
				return policy
			}
			initial := gateway.ProxyPolicyUpdate{Namespace: cfg.Namespace, Proxies: []string{"10.1.2.3"}}
			if w := request(http.MethodPut, initial); w.Code != 200 {
				t.Fatalf("initial PUT: %d %s", w.Code, w.Body.String())
			}
			revisions := map[string]bool{}
			for i := range peers {
				_, data, _ := call(context.Background(), i, http.MethodGet, "/v1/proxy-policy", nil)
				var policy gateway.ProxyPolicy
				if err := json.Unmarshal(data, &policy); err != nil {
					t.Fatal(err)
				}
				revisions[policy.Revision] = true
			}
			if len(revisions) != len(peers) {
				t.Fatal("test requires independently generated peer revisions")
			}
			update := gateway.ProxyPolicyUpdate{Namespace: cfg.Namespace, ExpectedRevision: current().Revision, Proxies: []string{"10.1.2.4"}}
			if partialFailure {
				fail = true
				if w := request(http.MethodPut, update); w.Code != 503 {
					t.Fatalf("partial failure acknowledged: %d %s", w.Code, w.Body.String())
				}
				fail = false
				update.ExpectedRevision = current().Revision
			}
			w := request(http.MethodPut, update)
			if w.Code != 200 {
				t.Fatalf("subsequent PUT: %d %s", w.Code, w.Body.String())
			}
			var ack gateway.ProxyPolicy
			if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil {
				t.Fatal(err)
			}
			if ack.Contract != 1 || ack.Namespace != cfg.Namespace || ack.Revision == "" || strings.Join(ack.Proxies, ",") != "10.1.2.4" || ack.ExpiresAtMS <= time.Now().Add(10*time.Second).UnixMilli() {
				t.Fatalf("invalid pool acknowledgement: %+v", ack)
			}
			if ack.Revision != current().Revision {
				t.Fatal("pool acknowledgement is not slot0 revision")
			}
			for i := range peers {
				_, data, _ := call(context.Background(), i, http.MethodGet, "/v1/proxy-policy", nil)
				var policy gateway.ProxyPolicy
				if err := json.Unmarshal(data, &policy); err != nil {
					t.Fatal(err)
				}
				if strings.Join(policy.Proxies, ",") != "10.1.2.4" {
					t.Fatalf("slot%d not repaired: %+v", i, policy)
				}
			}
			before := puts
			if w := request(http.MethodPut, update); w.Code != 409 {
				t.Fatalf("stale caller CAS accepted: %d %s", w.Code, w.Body.String())
			}
			if puts != before {
				t.Fatal("stale caller CAS mutated peers")
			}
		})
	}
}

func TestPoolProxyPolicyRejectsInvalidPeerAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gateway.ProxyPolicy)
	}{
		{"contract", func(p *gateway.ProxyPolicy) { p.Contract = 2 }},
		{"namespace", func(p *gateway.ProxyPolicy) { p.Namespace = "other" }},
		{"addresses", func(p *gateway.ProxyPolicy) { p.Proxies = []string{"10.9.9.9"} }},
		{"expired", func(p *gateway.ProxyPolicy) { p.ExpiresAtMS = time.Now().Add(-time.Second).UnixMilli() }},
		{"overlong_lease", func(p *gateway.ProxyPolicy) { p.ExpiresAtMS = time.Now().Add(time.Hour).UnixMilli() }},
		{"missing_revision", func(p *gateway.ProxyPolicy) { p.Revision = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gateway.Config{ControlToken: "secret", Namespace: "tenant", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}
			pool, err := NewPool(cfg, 2, func(_ context.Context, index int, method, _ string, _ []byte) (int, []byte, error) {
				policy := gateway.ProxyPolicy{Contract: 1, Namespace: cfg.Namespace, Revision: "old"}
				if method == http.MethodPut {
					policy.Revision = "new"
					policy.Proxies = []string{"10.1.2.4"}
					policy.ExpiresAtMS = time.Now().Add(time.Minute).UnixMilli()
					if index == 1 {
						tc.mutate(&policy)
					}
				}
				data, _ := json.Marshal(policy)
				return 200, data, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPut, "/v1/proxy-policy", strings.NewReader(`{"expected_revision":"old","namespace":"tenant","proxies":["10.1.2.4"]}`))
			r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
			w := httptest.NewRecorder()
			pool.ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("invalid peer acknowledged: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPoolProxyPolicySerializesCallerCAS(t *testing.T) {
	cfg := gateway.Config{ControlToken: "secret", Namespace: "tenant", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	var revision atomic.Int64
	var reads atomic.Int64
	firstRead, release, secondRead := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	pool, err := NewPool(cfg, 1, func(_ context.Context, _ int, method, _ string, body []byte) (int, []byte, error) {
		if method == http.MethodGet {
			if reads.Add(1) == 1 {
				close(firstRead)
				<-release
			} else {
				select {
				case secondRead <- struct{}{}:
				default:
				}
			}
		} else {
			var update gateway.ProxyPolicyUpdate
			if err := json.Unmarshal(body, &update); err != nil {
				return 400, nil, nil
			}
			if update.ExpectedRevision != strconv.FormatInt(revision.Load(), 10) {
				return 409, nil, nil
			}
			revision.Add(1)
		}
		data, _ := json.Marshal(gateway.ProxyPolicy{Contract: 1, Namespace: cfg.Namespace, Revision: strconv.FormatInt(revision.Load(), 10), Proxies: []string{"10.1.2.4"}, ExpiresAtMS: time.Now().Add(time.Minute).UnixMilli()})
		return 200, data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	request := func() {
		r := httptest.NewRequest(http.MethodPut, "/v1/proxy-policy", strings.NewReader(`{"expected_revision":"0","namespace":"tenant","proxies":["10.1.2.4"]}`))
		r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
		w := httptest.NewRecorder()
		pool.ServeHTTP(w, r)
		results <- w.Code
	}
	go request()
	<-firstRead
	go request()
	select {
	case <-secondRead:
		close(release)
		t.Fatal("concurrent PUT entered peer CAS transaction")
	case <-time.After(100 * time.Millisecond):
		close(release)
	}
	if first, second := <-results, <-results; first+second != 200+409 || revision.Load() != 1 {
		t.Fatalf("concurrent CAS statuses %d,%d revision%d", first, second, revision.Load())
	}
}
