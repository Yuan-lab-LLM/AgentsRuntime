package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
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

func (h *healthChecker) CheckHealth(ctx context.Context, spec gateway.GatewayStartSpec) error {
	return h.probeOnce(ctx, spec)
}

func (h *healthChecker) probeOnce(ctx context.Context, spec gateway.GatewayStartSpec) error {
	address := fmt.Sprintf("127.0.0.1:%d", spec.Port)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("opencode port %d is not listening: %w", spec.Port, err)
	}
	_ = conn.Close()

	username, password := authFromSpecEnv(spec.Env)
	v2 := false
	for _, item := range spec.Env {
		if item == "OPENCODE_RUNTIME_MAJOR=2" {
			v2 = true
		}
	}
	endpoint := "/global/health"
	if v2 {
		endpoint = "/api/info"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+endpoint, nil)
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
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode health returned %d", resp.StatusCode)
	}
	var payload struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
		Healthy bool   `json:"healthy"`
		OK      bool   `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return fmt.Errorf("opencode health invalid response: %w", err)
	}
	if v2 {
		if !strings.HasPrefix(payload.Version, "2.") || payload.PID <= 0 {
			return fmt.Errorf("opencode v2 server info is not ready")
		}
	} else if !payload.Healthy && !payload.OK {
		return fmt.Errorf("opencode v1 health is not ready")
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
