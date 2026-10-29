package openclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"io"
	"net/http"
	"time"
)

type nativeAdmissionChecker struct{ client *http.Client }

func newHealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	if cfg.RuntimeType != "openclaw" || !IsOpenClawAtLeast(cfg.OpenClawVersion, "2026.10.2") {
		return gateway.NewHTTPGatewayHealthChecker(cfg)
	}
	native := &nativeAdmissionChecker{client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return gateway.NewHTTPGatewayHealthCheckerWithProbe(cfg, func(ctx context.Context, spec gateway.GatewayStartSpec) error {
		base := fmt.Sprintf("http://127.0.0.1:%d", spec.Port)
		if err := native.probeOpenClawAdmission(ctx, base+"/startupz", "ok"); err != nil {
			return err
		}
		return native.probeOpenClawAdmission(ctx, base+"/readyz", "ready")
	})
}
func (h *nativeAdmissionChecker) probeOpenClawAdmission(ctx context.Context, rawURL, flag string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("OpenClaw admission probe unavailable: %w", err)
	}
	defer resp.Body.Close()
	var result map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("OpenClaw admission probe returned invalid JSON: %w", err)
	}
	var ready bool
	if err := json.Unmarshal(result[flag], &ready); err != nil || !ready || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenClaw admission is not ready (%s, http %d)", flag, resp.StatusCode)
	}
	return nil
}
