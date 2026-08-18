package opencode

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/internal/gateway"
)

type healthChecker struct {
	cfg    gateway.Config
	client *http.Client
}

func newHealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return &healthChecker{
		cfg: cfg,
		client: &http.Client{
			Timeout: 2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *healthChecker) WaitReady(ctx context.Context, spec gateway.GatewayStartSpec) error {
	timeout := h.cfg.GatewayStartupTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("opencode gateway not ready within %s", timeout)
		}
		if err := h.probeOnce(ctx, spec); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (h *healthChecker) probeOnce(ctx context.Context, spec gateway.GatewayStartSpec) error {
	address := fmt.Sprintf("127.0.0.1:%d", spec.Port)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("opencode port %d is not listening: %w", spec.Port, err)
	}
	_ = conn.Close()

	username, password := authFromSpecEnv(spec.Env)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/global/health", nil)
	if err != nil {
		return err
	}
	if password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("opencode health unavailable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("opencode health returned %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("opencode health unauthorized")
	}
	return nil
}

func authFromSpecEnv(env []string) (username, password string) {
	username = "opencode"
	for _, item := range env {
		switch {
		case strings.HasPrefix(item, "OPENCODE_SERVER_USERNAME="):
			if value := strings.TrimSpace(strings.TrimPrefix(item, "OPENCODE_SERVER_USERNAME=")); value != "" {
				username = value
			}
		case strings.HasPrefix(item, "OPENCODE_SERVER_PASSWORD="):
			password = strings.TrimPrefix(item, "OPENCODE_SERVER_PASSWORD=")
		}
	}
	return username, password
}
