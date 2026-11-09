package instanceagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("agent session unauthorized")

type apiClient struct {
	cfg    Config
	client *http.Client
}

func newAPIClient(cfg Config) *apiClient {
	return &apiClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (c *apiClient) register(ctx context.Context) (registerResponse, error) {
	body := map[string]any{
		"instance_id":      c.cfg.InstanceIDValue(),
		"agent_id":         c.cfg.AgentID,
		"agent_version":    c.cfg.AgentVersion,
		"protocol_version": c.cfg.ProtocolVersion,
		"capabilities": []string{
			"runtime.status",
			"runtime.health",
			"metrics.report",
			"skills.inventory",
			"skills.upload",
			"skills.install",
			"skills.remove",
			"commands.poll",
		},
		"host_info": map[string]any{
			"runtime":        "hermes",
			"desktop_base":   "webtop",
			"persistent_dir": c.cfg.PersistentDir,
			"port":           3001,
			"arch":           goArch(),
		},
	}

	var response registerResponse
	err := c.postJSON(ctx, "register", c.cfg.BootstrapToken, body, &response)
	return response, err
}

func (c *apiClient) heartbeat(ctx context.Context, token string, body HeartbeatBody) (heartbeatResponse, error) {
	var response heartbeatResponse
	err := c.postJSON(ctx, "heartbeat", token, body, &response)
	return response, err
}

func (c *apiClient) reportState(ctx context.Context, token string, body StateReport) error {
	return c.postJSON(ctx, "state/report", token, body, nil)
}

func (c *apiClient) reportSkills(ctx context.Context, token string, body SkillInventoryBody) error {
	return c.postJSON(ctx, "skills/inventory", token, body, nil)
}

func (c *apiClient) nextCommand(ctx context.Context, token string) (*Command, error) {
	var response nextCommandResponse
	if err := c.getJSON(ctx, "commands/next", token, &response); err != nil {
		return nil, err
	}
	return response.Command, nil
}

func (c *apiClient) startCommand(ctx context.Context, token, id, agentID string) error {
	body := map[string]any{
		"agent_id":   agentID,
		"started_at": time.Now().UTC(),
	}
	return c.postJSON(ctx, "commands/"+id+"/start", token, body, nil)
}

func (c *apiClient) finishCommand(ctx context.Context, token, id string, body commandFinishBody) error {
	return c.postJSON(ctx, "commands/"+id+"/finish", token, body, nil)
}

func (c *apiClient) uploadSkill(ctx context.Context, token string, skill SkillInfo, zipPath string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fields := map[string]string{
		"agent_id":      c.cfg.AgentID,
		"skill_id":      skill.SkillID,
		"skill_version": skill.SkillVersion,
		"identifier":    skill.Identifier,
		"content_md5":   skill.ContentMD5,
		"source":        skill.Source,
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}

	file, err := os.Open(zipPath)
	if err != nil {
		return err
	}
	defer file.Close()

	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AgentAPIURL("skills/upload"), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.do(req, nil)
}

func (c *apiClient) downloadSkill(ctx context.Context, token, skillVersion string) ([]byte, error) {
	if skillVersion == "" || strings.ContainsAny(skillVersion, "/\\?#") {
		return nil, errors.New("invalid skill version")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.AgentAPIURL("skills/versions/"+skillVersion+"/download"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s failed: status=%d body=%s", req.URL.Path, resp.StatusCode, body)
	}
	const maxSkillArchiveBytes = 256 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSkillArchiveBytes {
		return nil, errors.New("skill archive exceeds 256 MiB limit")
	}
	return body, nil
}

func (c *apiClient) postJSON(ctx context.Context, path, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AgentAPIURL(path), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *apiClient) getJSON(ctx context.Context, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.AgentAPIURL(path), nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *apiClient) do(req *http.Request, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: status=%d body=%s", req.Method, req.URL.Path, resp.StatusCode, truncate(body, 512))
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return decodeAPIResponse(body, out)
}

func decodeAPIResponse(body []byte, out any) error {
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Data != nil {
		if envelope.Error != "" {
			return errors.New(envelope.Error)
		}
		if string(envelope.Data) == "null" {
			return nil
		}
		return json.Unmarshal(envelope.Data, out)
	}
	return json.Unmarshal(body, out)
}

func truncate(body []byte, max int) string {
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "..."
}
