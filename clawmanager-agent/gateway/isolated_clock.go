package gateway

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// RefreshClock compares against the internal control-plane HTTP server's Date
// header without sending credentials or changing the container's clock.
func (m *GatewayManager) RefreshClock(ctx context.Context) error {
	if !m.IsolatedGatewayLifecycle() {
		return nil
	}
	m.mu.RLock()
	checked, issue := m.clockCheckedAt, m.clockIssue
	m.mu.RUnlock()
	if !checked.IsZero() && time.Since(checked) < 15*time.Second {
		if issue != "" {
			return fmt.Errorf("%s", issue)
		}
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodHead, m.cfg.BackendURL, nil)
	if err != nil {
		return m.recordClockIssue("clock_reference_unavailable")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return m.recordClockIssue("clock_reference_unavailable")
	}
	defer response.Body.Close()
	serverTime, err := http.ParseTime(response.Header.Get("Date"))
	if err != nil {
		return m.recordClockIssue("clock_reference_unavailable")
	}
	skew := serverTime.Sub(started.Add(time.Since(started) / 2))
	if skew < 0 {
		skew = -skew
	}
	if skew > 120*time.Second {
		return m.recordClockIssue("clock_skew")
	}
	if skew > 30*time.Second {
		log.Printf("runtime-agent clock warning: category=clock_skew seconds=%d", int(skew.Seconds()))
	}
	return m.recordClockIssue("")
}

func (m *GatewayManager) recordClockIssue(issue string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if issue == "clock_reference_unavailable" && m.clockIssue != "clock_skew" && !m.clockLastGood.IsZero() && time.Since(m.clockLastGood) < 5*time.Minute {
		// A brief control-plane outage is not evidence that the kernel clock
		// jumped. Retain a bounded, previously verified sample.
		m.clockCheckedAt = time.Now()
		return nil
	}
	if issue == "" {
		m.clockLastGood = time.Now()
	}
	m.clockRequired, m.clockCheckedAt, m.clockIssue = true, time.Now(), issue
	m.notifyGatewayStateChangedLocked()
	if issue != "" {
		return fmt.Errorf("%s", issue)
	}
	return nil
}
