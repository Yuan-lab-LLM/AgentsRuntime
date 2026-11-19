package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errCapabilityNotReady = errors.New("OpenClaw capability registration is not ready")

func capabilityHealthRequired(env []string) bool {
	for _, v := range env {
		if v == renewalFlag+"=1" {
			return true
		}
	}
	return false
}
func checkCapabilityHealth(ctx context.Context, env []string) error {
	flags, err := capabilityFlags(env)
	if err != nil {
		return errCapabilityNotReady
	}
	if flags[renewalFlag] != "1" {
		return nil
	}
	values := map[string]string{}
	for _, v := range env {
		k, value, _ := strings.Cut(v, "=")
		if k != "CLAWMANAGER_LLM_BASE_URL" && k != "CLAWMANAGER_LLM_API_KEY" && k != "CLAWMANAGER_GATEWAY_GENERATION" {
			continue
		}
		if _, exists := values[k]; exists {
			return errCapabilityNotReady
		}
		values[k] = value
	}
	raw := values["CLAWMANAGER_LLM_BASE_URL"]
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errCapabilityNotReady
	}
	key := values["CLAWMANAGER_LLM_API_KEY"]
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return errCapabilityNotReady
	}
	g, err := strconv.ParseInt(values["CLAWMANAGER_GATEWAY_GENERATION"], 10, 32)
	if err != nil || g < 0 || strconv.FormatInt(g, 10) != values["CLAWMANAGER_GATEWAY_GENERATION"] {
		return errCapabilityNotReady
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(deadline, http.MethodGet, strings.TrimRight(raw, "/")+"/runtime/capabilities", nil)
	if err != nil {
		return errCapabilityNotReady
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errCapabilityNotReady
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errCapabilityNotReady
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errCapabilityNotReady
	}
	var result struct {
		Protocol             int    `json:"protocol"`
		Generation           *int64 `json:"generation"`
		Enrolled             bool   `json:"enrolled"`
		EnrollmentGeneration *int64 `json:"enrollment_generation"`
		Sticky               bool   `json:"registration_is_sticky"`
		Report               *struct {
			Runtime string `json:"runtime_version"`
			Adapter string `json:"adapter_version"`
		} `json:"current_report"`
	}
	if json.Unmarshal(data, &result) != nil || result.Protocol != 1 || result.Generation == nil || *result.Generation != g || !result.Enrolled || result.EnrollmentGeneration == nil || *result.EnrollmentGeneration != g || !result.Sticky || result.Report == nil || result.Report.Runtime != "2026.10.2" || result.Report.Adapter != "cm-openclaw-native-1" {
		return errCapabilityNotReady
	}
	return nil
}
