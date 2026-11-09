package gateway

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPGatewayHealthCheckerUsesNonUpgradingOriginProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	upgradeSeen := false
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "" {
				upgradeSeen = true
			}
			w.WriteHeader(http.StatusOK)
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	port := listener.Addr().(*net.TCPAddr).Port
	checker := NewHTTPGatewayHealthChecker(Config{
		PublicOrigin:          "http://clawmanager-gateway.clawmanager-system.svc.cluster.local:9001",
		GatewayStartupTimeout: time.Second,
	})

	if err := checker.WaitReady(context.Background(), GatewayStartSpec{Port: port}); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if upgradeSeen {
		t.Fatal("WaitReady() sent a websocket upgrade probe, want ordinary HTTP origin check only")
	}
}

func TestHTTPGatewayHealthCheckerTreatsAuthRedirectAsReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	loginHit := false
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/login" && r.URL.Query().Get("provider") == "basic" {
				loginHit = true
				http.Error(w, "BasicAuthProvider is password-only", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/auth/login?provider=basic", http.StatusFound)
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	port := listener.Addr().(*net.TCPAddr).Port
	checker := NewHTTPGatewayHealthChecker(Config{GatewayStartupTimeout: time.Second})

	if err := checker.WaitReady(context.Background(), GatewayStartSpec{Port: port}); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if loginHit {
		t.Fatal("WaitReady() followed auth redirect to /auth/login?provider=basic")
	}
}

func TestHTTPGatewayHealthCheckerAuthStatusesReadyButServerErrorsFail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "redirect", status: http.StatusFound},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "server error", status: http.StatusInternalServerError, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer listener.Close()

			server := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
				}),
			}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Shutdown(context.Background())

			port := listener.Addr().(*net.TCPAddr).Port
			checker := NewHTTPGatewayHealthChecker(Config{})
			err = checker.probeOnce(context.Background(), GatewayStartSpec{Port: port})
			if tc.wantErr && err == nil {
				t.Fatal("WaitReady() error = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("WaitReady() error = %v", err)
			}
		})
	}
}
