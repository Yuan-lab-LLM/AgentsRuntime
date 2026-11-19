package netbroker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFixedEgressUsesTrustedCredentialOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://public.example/own" || r.Header.Get("Proxy-Authorization") != "Bearer bw1.trusted-sentinel" || r.Header.Get("Authorization") != "Bearer own-origin-key" {
			t.Error("fixed egress credential or target boundary lost")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	b, err := NewFixedEgress(context.Background(), "127.0.0.1", upstream.URL, "bw1.trusted-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	r := httptest.NewRequest("GET", "http://public.example/own", nil)
	r.RemoteAddr = "127.0.0.1:4242"
	r.Header.Set("Proxy-Authorization", "Bearer forged")
	r.Header.Set("Authorization", "Bearer own-origin-key")
	w := httptest.NewRecorder()
	b.Egress().ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatal("fixed egress failed")
	}
	for _, credential := range []string{"", "bad\r\nheader"} {
		if bad, err := NewFixedEgress(context.Background(), "127.0.0.1", upstream.URL, credential); err == nil {
			bad.Close()
			t.Fatal("unsafe credential accepted")
		}
	}
}

func TestBrokerRejectsTypedNilCredentialSource(t *testing.T) {
	var credentials *Credentials
	if b, err := New(context.Background(), Config{ChildIP: "127.0.0.1", BackendURL: "http://backend.invalid", EgressProxyURL: "http://proxy.invalid", Credentials: credentials}); err == nil {
		b.Close()
		t.Fatal("nil credential source admitted")
	}
}
