package netbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testIdentity = Identity{InstanceID: 7, UserID: 3, Generation: 2, PodUID: "pod-7"}

func testLease() Lease {
	return Lease{Identity: testIdentity, ModelToken: "v1.model", EgressToken: "v1.egress", ServicesToken: "v1.services", ExpiresAt: time.Now().Add(10 * time.Minute)}
}
func leaseServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}
func TestAcquireUsesFixedControlEndpointAndStrictLease(t *testing.T) {
	calls := 0
	server := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/runtime/security/lease" || r.Header.Get("X-ClawManager-Control-Token") != "control-sentinel" || r.Header.Get("Authorization") != "" {
			t.Errorf("bad lease request: method=%s path=%s", r.Method, r.URL.Path)
		}
		var id Identity
		if json.NewDecoder(r.Body).Decode(&id) != nil || id != testIdentity {
			t.Errorf("wrong lease identity: %+v", id)
		}
		json.NewEncoder(w).Encode(testLease())
	})
	lease, err := Acquire(context.Background(), server.URL, "control-sentinel", testIdentity)
	if err != nil || lease.Identity != testIdentity || lease.ModelToken != "v1.model" || calls != 1 {
		t.Fatalf("lease=%+v calls=%d err=%v", lease.Identity, calls, err)
	}
	for _, scenario := range []string{"identity", "expired", "overlong-life", "empty", "newline", "oversize-token", "unknown", "trailing", "oversize-body", "non200"} {
		t.Run(scenario, func(t *testing.T) {
			s := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				lease := testLease()
				switch scenario {
				case "identity":
					lease.Generation++
				case "expired":
					lease.ExpiresAt = time.Now().Add(-time.Second)
				case "overlong-life":
					lease.ExpiresAt = time.Now().Add(16 * time.Minute)
				case "empty":
					lease.ModelToken = ""
				case "newline":
					lease.EgressToken = "v1.secret\r\nInjected: bad"
				case "oversize-token":
					lease.ServicesToken = "v1." + strings.Repeat("x", 4096)
				case "non200":
					w.WriteHeader(500)
					fmt.Fprint(w, "secret-response control-sentinel")
					return
				}
				b, _ := json.Marshal(lease)
				switch scenario {
				case "unknown":
					b = append(b[:len(b)-1], []byte(`,"unexpected":"secret-response"}`)...)
				case "trailing":
					b = append(b, []byte(`{}`)...)
				case "oversize-body":
					b = append(b, []byte(strings.Repeat(" ", 16385))...)
				}
				w.Write(b)
			})
			_, err := Acquire(context.Background(), s.URL, "control-sentinel", testIdentity)
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("unsafe lease accepted or leaked: %v", err)
			}
		})
	}
}
func TestAcquireRejectsRedirectAndUnsafeConfiguration(t *testing.T) {
	var leaked atomic.Bool
	target := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
		json.NewEncoder(w).Encode(testLease())
	})
	redirect := leaseServer(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	if _, err := Acquire(context.Background(), redirect.URL, "control-sentinel", testIdentity); err == nil || leaked.Load() {
		t.Fatal("lease redirect followed")
	}
	for _, base := range []string{target.URL + "/path", target.URL + "?q=x", target.URL + "#fragment", "http://user:credential@127.0.0.1", "ftp://127.0.0.1", "http://127.0.0.1/%2e", ""} {
		if _, err := Acquire(context.Background(), base, "control-sentinel", testIdentity); err == nil {
			t.Errorf("unsafe backend accepted: %q", base)
		}
	}
}
func TestCredentialsRenewAndFailClosed(t *testing.T) {
	var calls atomic.Int32
	server := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n >= 3 {
			w.WriteHeader(503)
			return
		}
		lease := testLease()
		lease.ModelToken = fmt.Sprintf("v1.model%d", n)
		json.NewEncoder(w).Encode(lease)
	})
	creds, err := newCredentials(context.Background(), server.URL, "control-sentinel", testIdentity, credentialTiming{renew: 20 * time.Millisecond, now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	deadline := time.Now().Add(time.Second)
	for {
		lease, err := creds.Snapshot()
		if err == nil && lease.ModelToken == "v1.model2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("credentials did not renew")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-creds.Done():
	case <-time.After(time.Second):
		t.Fatal("failed refresh did not stop lifetime")
	}
	if _, err := creds.Snapshot(); err == nil || creds.Err() == nil || creds.Context().Err() == nil {
		t.Fatal("refresh failure kept authorization")
	}
	creds.mu.Lock()
	if creds.lease.ModelToken != "" || creds.lease.EgressToken != "" || creds.lease.ServicesToken != "" {
		t.Error("failed credentials were not cleared")
	}
	creds.mu.Unlock()
}
func TestCredentialsExpireAndParentStop(t *testing.T) {
	for _, parentStop := range []bool{false, true} {
		t.Run(fmt.Sprint(parentStop), func(t *testing.T) {
			server := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				lease := testLease()
				lease.ExpiresAt = time.Now().Add(50 * time.Millisecond)
				json.NewEncoder(w).Encode(lease)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			creds, err := NewCredentials(ctx, server.URL, "control-sentinel", testIdentity)
			if err != nil {
				t.Fatal(err)
			}
			defer creds.Close()
			if parentStop {
				cancel()
			}
			select {
			case <-creds.Done():
			case <-time.After(time.Second):
				t.Fatal("expiry/parent stop left manager running")
			}
			if _, err := creds.Snapshot(); err == nil {
				t.Fatal("dead manager returned capability")
			}
		})
	}
}

func TestExpiryInterruptsBlockedRefresh(t *testing.T) {
	var calls atomic.Int32
	canceled := make(chan struct{})
	server := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			close(canceled)
			return
		}
		lease := testLease()
		lease.ExpiresAt = time.Now().Add(100 * time.Millisecond)
		json.NewEncoder(w).Encode(lease)
	})
	creds, err := newCredentials(context.Background(), server.URL, "control-sentinel", testIdentity, credentialTiming{renew: 10 * time.Millisecond, now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer creds.Close()
	select {
	case <-creds.Done():
	case <-time.After(time.Second):
		t.Fatal("blocked refresh extended expired lifetime")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("blocked lease HTTP request retained")
	}
	if _, err := creds.Snapshot(); err == nil {
		t.Fatal("expired refresh retained capabilities")
	}
}
