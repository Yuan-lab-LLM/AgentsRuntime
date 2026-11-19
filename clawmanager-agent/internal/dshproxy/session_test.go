package dshproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserSessionRefreshesOnceBeforeConcurrentDispatch(t *testing.T) {
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "secret" {
			t.Error("missing bootstrap token")
		}
		version := exchanges.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "dsh-auth-test", Value: fmt.Sprint(version), MaxAge: 3600})
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()
	launch, _ := url.Parse(server.URL + "/?token=secret")
	s := &browserSession{launchURL: launch}
	if _, err := s.cookie(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.refreshAt = time.Now().Add(-time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.cookie(context.Background())
			if err != nil || got != "dsh-auth-test=2" {
				t.Errorf("cookie=%q err=%v", got, err)
			}
		}()
	}
	wg.Wait()
	if got := exchanges.Load(); got != 2 {
		t.Fatalf("exchanges=%d, want 2", got)
	}
}

func TestDesktopProxyRejectsCrossOriginAndRemoteRequests(t *testing.T) {
	for _, tc := range []struct {
		name, remote, host, origin, fetchSite string
		allowed                               bool
	}{
		{"local", "127.0.0.1:1234", "127.0.0.1:3080", "", "", true},
		{"same origin", "127.0.0.1:1234", "127.0.0.1:3080", "http://127.0.0.1:3080", "same-origin", true},
		{"ipv6", "[::1]:1234", "[::1]:3080", "http://[::1]:3080", "", true},
		{"remote", "10.0.0.2:1234", "127.0.0.1:3080", "", "", false},
		{"rebind", "127.0.0.1:1234", "attacker.example:3080", "", "", false},
		{"cross origin", "127.0.0.1:1234", "127.0.0.1:3080", "https://attacker.example", "", false},
		{"cross site navigation", "127.0.0.1:1234", "127.0.0.1:3080", "", "cross-site", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			if got := trustedDesktopRequest(r); got != tc.allowed {
				t.Fatalf("allowed=%v", got)
			}
		})
	}
}

func TestDesktopFlagCannotExposeUnauthenticatedListener(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "localhost", "10.0.0.1"} {
		err := RunCommand(context.Background(), []string{"--desktop", "--listen-host", host, "--port", "3080", "--", "unused"}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("host=%s error=%v", host, err)
		}
	}
}

func TestBootstrapNetworkErrorDoesNotLeakToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	launch, _ := url.Parse("http://127.0.0.1:1/?token=do-not-log")
	_, _, err := bootstrapBrowserSession(ctx, launch)
	if err == nil || strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("unsafe error: %v", err)
	}
}
