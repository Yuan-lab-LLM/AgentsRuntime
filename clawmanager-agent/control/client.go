package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type ReportClient struct {
	baseURL         string
	token           string
	httpClient      *http.Client
	heartbeatClient *http.Client
}

func NewReportClient(cfg gateway.Config) *ReportClient {
	return &ReportClient{
		baseURL: strings.TrimRight(cfg.BackendURL, "/"),
		token:   cfg.ReportToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		heartbeatClient: &http.Client{
			Timeout: heartbeatRequestTimeout(cfg.HeartbeatInterval),
		},
	}
}

func heartbeatRequestTimeout(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	timeout := interval / 2
	if timeout <= 0 {
		timeout = interval
	}
	if timeout > time.Second {
		timeout = time.Second
	}
	return timeout
}

func (c *ReportClient) Register(ctx context.Context, payload gateway.RegisterPayload) (int, error) {
	var resp gateway.RegisterResponse
	if err := c.doJSON(ctx, "/api/v1/runtime-agent/register", payload, &resp); err != nil {
		return 0, err
	}
	if resp.PodID != 0 {
		return resp.PodID, nil
	}
	return resp.Pod.ID, nil
}

func (c *ReportClient) ReportHeartbeat(ctx context.Context, payload gateway.HeartbeatPayload) error {
	return c.doJSONWithClient(ctx, c.heartbeatClient, "/api/v1/runtime-agent/heartbeat", payload, nil)
}

func (c *ReportClient) ReportMetrics(ctx context.Context, payload gateway.MetricsPayload) error {
	return c.doJSON(ctx, "/api/v1/runtime-agent/metrics/report", payload, nil)
}

func (c *ReportClient) ReportGateways(ctx context.Context, payload gateway.GatewayReportPayload) error {
	return c.doJSON(ctx, "/api/v1/runtime-agent/gateways/report", payload, nil)
}

func (c *ReportClient) ReportSkills(ctx context.Context, payload gateway.SkillReportPayload) error {
	return c.doJSON(ctx, "/api/v1/runtime-agent/skills/report", payload, nil)
}

func (c *ReportClient) doJSON(ctx context.Context, endpoint string, payload any, response any) error {
	return c.doJSONWithClient(ctx, c.httpClient, endpoint, payload, response)
}

func (c *ReportClient) doJSONWithClient(ctx context.Context, client *http.Client, endpoint string, payload any, response any) error {
	buf := &bytes.Buffer{}
	if payload != nil {
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, buf)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set(gateway.AgentTokenHeader, c.token)
	}

	if client == nil {
		return fmt.Errorf("post %s: http client is not configured", endpoint)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(data))
	}
	if response == nil || len(data) == 0 {
		return nil
	}

	var envelope struct {
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && (envelope.Success || envelope.Error != "" || len(envelope.Data) > 0) {
		if envelope.Error != "" {
			return fmt.Errorf("api error: %s", envelope.Error)
		}
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return nil
		}
		return json.Unmarshal(envelope.Data, response)
	}
	return json.Unmarshal(data, response)
}
