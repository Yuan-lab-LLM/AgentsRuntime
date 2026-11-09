package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type noopGatewayHealthChecker struct {
}

func NewNoopGatewayHealthChecker() GatewayHealthChecker {
	return noopGatewayHealthChecker{}
}

func (noopGatewayHealthChecker) WaitReady(context.Context, GatewayStartSpec) error {
	return nil
}

func (noopGatewayHealthChecker) CheckHealth(context.Context, GatewayStartSpec) error {
	return nil
}

type HTTPGatewayHealthChecker struct {
	cfg        Config
	client     *http.Client
	extraProbe func(context.Context, GatewayStartSpec) error
}

func NewHTTPGatewayHealthChecker(cfg Config) *HTTPGatewayHealthChecker {
	return &HTTPGatewayHealthChecker{
		cfg: cfg,
		client: &http.Client{
			Timeout: 2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *HTTPGatewayHealthChecker) WaitReady(ctx context.Context, spec GatewayStartSpec) error {
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
			return fmt.Errorf("gateway not ready within %s", timeout)
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

func (h *HTTPGatewayHealthChecker) CheckHealth(ctx context.Context, spec GatewayStartSpec) error {
	return h.probeOnce(ctx, spec)
}

func (h *HTTPGatewayHealthChecker) probeOnce(ctx context.Context, spec GatewayStartSpec) error {
	address := fmt.Sprintf("127.0.0.1:%d", spec.Port)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("gateway port %d is not listening: %w", spec.Port, err)
	}
	_ = conn.Close()

	baseURL := "http://" + address
	if err := h.probeHTTP(ctx, baseURL+"/", false); err != nil {
		return err
	}
	if h.cfg.PublicOrigin != "" && h.cfg.PublicOrigin != "*" {
		if err := h.probeHTTP(ctx, baseURL+"/", true); err != nil {
			return err
		}
	}
	if h.extraProbe != nil {
		return h.extraProbe(ctx, spec)
	}
	return nil
}

func (h *HTTPGatewayHealthChecker) probeHTTP(ctx context.Context, rawURL string, withOrigin bool, extraHeaders ...map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if withOrigin {
		req.Header.Set("Origin", h.cfg.PublicOrigin)
	}
	for _, headers := range extraHeaders {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway http %s unavailable: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if strings.Contains(strings.ToLower(string(body)), "origin not allowed") {
		return fmt.Errorf("gateway rejected public origin %s: %s", h.cfg.PublicOrigin, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("gateway http %s returned %d", rawURL, resp.StatusCode)
	}
	return nil
}

func NewHTTPGatewayHealthCheckerWithProbe(cfg Config, probe func(context.Context, GatewayStartSpec) error) *HTTPGatewayHealthChecker {
	checker := NewHTTPGatewayHealthChecker(cfg)
	checker.extraProbe = probe
	return checker
}
