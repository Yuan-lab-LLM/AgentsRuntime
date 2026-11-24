package netbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenewalTransientFailureKeepsLeaseAndRecoversWithoutRestart(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			failed := make(chan struct{})
			s := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 2 {
					w.WriteHeader(status)
					close(failed)
					return
				}
				lease := testLease()
				if n >= 3 {
					lease.ModelToken = "v1.recovered"
				}
				json.NewEncoder(w).Encode(lease)
			})
			c, err := newCredentials(context.Background(), s.URL, "control-sentinel", testIdentity, credentialTiming{renew: 20 * time.Millisecond, now: time.Now, retryMin: 50 * time.Millisecond, retryMax: 50 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			<-failed
			if _, err = c.Snapshot(); err != nil || c.Context().Err() != nil {
				t.Fatal("transient renewal destroyed valid lifetime", err)
			}
			deadline := time.After(time.Second)
			for {
				lease, err := c.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if lease.ModelToken == "v1.recovered" {
					break
				}
				select {
				case <-deadline:
					t.Fatal("renewal never recovered")
				default:
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}

func TestTransientRenewalCannotExtendExpiryAndCloseCancelsBackoff(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "expires", true: "cancel"}[closeEarly], func(t *testing.T) {
			var calls atomic.Int32
			s := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) > 1 {
					w.WriteHeader(503)
					return
				}
				lease := testLease()
				lease.ExpiresAt = time.Now().Add(140 * time.Millisecond)
				json.NewEncoder(w).Encode(lease)
			})
			c, err := newCredentials(context.Background(), s.URL, "control-sentinel", testIdentity, credentialTiming{renew: 10 * time.Millisecond, now: time.Now, retryMin: 20 * time.Millisecond, retryMax: 30 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if closeEarly {
				time.Sleep(30 * time.Millisecond)
				c.Close()
			}
			select {
			case <-c.Done():
			case <-time.After(time.Second):
				t.Fatal("expired/canceled retry kept lifetime")
			}
			if _, err = c.Snapshot(); err == nil {
				t.Fatal("expired lease retained")
			}
			if !closeEarly && !errors.Is(c.Err(), errLeaseExpired) {
				t.Fatal("expiry cause lost", c.Err())
			}
			if calls.Load() > 15 {
				t.Fatal("unbounded retry loop")
			}
		})
	}
}

func TestMalformedOrRevokedRefreshNeverRetries(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		var calls atomic.Int32
		s := leaseServer(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) > 1 {
				if malformed {
					w.Write([]byte(`{"model_token":"secret"}`))
				} else {
					w.WriteHeader(401)
				}
				return
			}
			json.NewEncoder(w).Encode(testLease())
		})
		c, err := newCredentials(context.Background(), s.URL, "control-sentinel", testIdentity, credentialTiming{renew: 10 * time.Millisecond, now: time.Now, retryMin: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.Done():
		case <-time.After(time.Second):
			t.Fatal("permanent failure retried")
		}
		c.Close()
		if calls.Load() != 2 {
			t.Fatal("permanent failure replayed", calls.Load())
		}
	}
}
