package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/shutdownoutbox"
)

type HTTPStatusError struct {
	Code int
	Body string
}

func (e HTTPStatusError) Error() string {
	return fmt.Sprintf("unexpected status %d: %s", e.Code, e.Body)
}

func (e HTTPStatusError) StatusCode() int {
	return e.Code
}

type Client struct {
	baseURL        string
	httpClient     *http.Client
	getToken       func() string
	bootstrapToken string
}

func New(baseURL string, bootstrapToken string, getToken func() string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		getToken:       getToken,
		bootstrapToken: strings.TrimSpace(bootstrapToken),
	}
}

func (c *Client) Register(ctx context.Context, req protocol.RegisterRequest) (protocol.RegisterResponse, error) {
	var resp protocol.RegisterResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/register", req, &resp, authBootstrap)
	return resp, err
}

func (c *Client) Heartbeat(ctx context.Context, req protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	var resp protocol.HeartbeatResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", req, &resp, authSession)
	return resp, err
}

func (c *Client) NextCommand(ctx context.Context) (*protocol.Command, error) {
	var resp protocol.CommandEnvelope
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/commands/next", nil, &resp, authSession); err != nil {
		return nil, err
	}
	return resp.Command, nil
}

func (c *Client) StartCommand(ctx context.Context, id int, req protocol.CommandStartRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/commands/"+url.PathEscape(fmt.Sprintf("%d", id))+"/start", req, nil, authSession)
}

func (c *Client) FinishCommand(ctx context.Context, id int, req protocol.CommandFinishRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/commands/"+url.PathEscape(fmt.Sprintf("%d", id))+"/finish", req, nil, authSession)
}

func (c *Client) ReportState(ctx context.Context, req protocol.StateReportRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/state/report", req, nil, authSession)
}

type ShutdownDeliveryScope struct {
	Version     int `json:"version"`
	InstanceID  int `json:"instance_id"`
	OwnerUserID int `json:"owner_user_id"`
}

func (c *Client) ShutdownDeliveryScope(ctx context.Context) (ShutdownDeliveryScope, error) {
	var scope ShutdownDeliveryScope
	if shutdownDeliveryJSON(ctx, c.httpClient, c.baseURL+"/api/v1/agent/shutdown-deliveries/scope", "Authorization", "Bearer "+c.getToken(), http.MethodGet, nil, &scope) != nil {
		return scope, fmt.Errorf("shutdown delivery scope unavailable")
	}
	return scope, nil
}
func (c *Client) ReportShutdownDeliveries(ctx context.Context, items []shutdownoutbox.Delivery) (shutdownoutbox.Ack, error) {
	var ack shutdownoutbox.Ack
	err := shutdownDeliveryJSON(ctx, c.httpClient, c.baseURL+"/api/v1/agent/shutdown-deliveries", "Authorization", "Bearer "+c.getToken(), http.MethodPost, struct {
		Deliveries []shutdownoutbox.Delivery `json:"deliveries"`
	}{items}, &ack)
	if err != nil || ack.Version != 1 || !ack.DiagnosticOnly || ack.AcceptedIDs == nil {
		return ack, fmt.Errorf("shutdown delivery unconfirmed")
	}
	return ack, nil
}

func (c *Client) ReportSkillInventory(ctx context.Context, req protocol.SkillInventoryReportRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/skills/inventory", req, nil, authSession)
}

func (c *Client) FetchConfigRevision(ctx context.Context, id string) (protocol.ConfigRevisionResponse, error) {
	var resp protocol.ConfigRevisionResponse
	err := c.doJSON(ctx, http.MethodGet, path.Join("/api/v1/agent/config/revisions", id), nil, &resp, authSession)
	return resp, err
}

func (c *Client) DownloadSkillArchive(ctx context.Context, skillVersion string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path.Join("/api/v1/agent/skills/versions", url.PathEscape(skillVersion), "download"), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	token := c.getToken()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download skill archive: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return nil, fmt.Errorf("read skill archive: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, HTTPStatusError{Code: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

func (c *Client) UploadSkillArchive(ctx context.Context, req protocol.SkillUploadRequest, fileName string, file io.Reader) error {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer writer.Close()

		part, err := writer.CreateFormFile("file", fileName)
		if err != nil {
			_ = pw.CloseWithError(fmt.Errorf("create form file: %w", err))
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			_ = pw.CloseWithError(fmt.Errorf("write archive body: %w", err))
			return
		}

		fields := map[string]string{
			"agent_id":      req.AgentID,
			"skill_id":      req.SkillID,
			"skill_version": req.SkillVersion,
			"identifier":    req.Identifier,
			"content_md5":   req.ContentMD5,
			"source":        req.Source,
		}
		for key, value := range fields {
			if strings.TrimSpace(value) == "" {
				continue
			}
			if err := writer.WriteField(key, value); err != nil {
				_ = pw.CloseWithError(fmt.Errorf("write field %s: %w", key, err))
				return
			}
		}
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/agent/skills/upload", pr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	token := c.getToken()
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("upload skill archive: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HTTPStatusError{Code: resp.StatusCode, Body: string(data)}
	}
	return nil
}

type authMode int

const (
	authNone authMode = iota
	authBootstrap
	authSession
)

type apiEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, reqBody any, respBody any, auth authMode) error {
	var body io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	switch auth {
	case authBootstrap:
		if c.bootstrapToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.bootstrapToken)
		}
	case authSession:
		token := c.getToken()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HTTPStatusError{Code: resp.StatusCode, Body: string(data)}
	}
	if respBody == nil || len(data) == 0 {
		return nil
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(data, &envelope); err == nil && (envelope.Success || envelope.Error != "" || len(envelope.Data) > 0) {
		if envelope.Error != "" {
			return fmt.Errorf("api error: %s", envelope.Error)
		}
		if respBody == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return nil
		}
		if err := json.Unmarshal(envelope.Data, respBody); err != nil {
			return fmt.Errorf("decode response data: %w", err)
		}
		return nil
	}
	if err := json.Unmarshal(data, respBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
