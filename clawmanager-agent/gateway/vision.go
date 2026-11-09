package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// nil means an older manager supplied no capability snapshot. An explicit
// false removes image input; missing entries preserve the previous setting.
func ParseVisionCapabilities(raw string) (map[string]bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, fmt.Errorf("invalid CLAWMANAGER_LLM_VISION object")
	}
	result := make(map[string]bool, len(values))
	for key, value := range values {
		if string(value) != "true" && string(value) != "false" {
			return nil, fmt.Errorf("invalid vision capability for %q", key)
		}
		result[key] = string(value) == "true"
	}
	return result, nil
}

func ResolveVisionCapabilities(cfg Config, req CreateGatewayRequest) (map[string]bool, error) {
	for _, env := range []map[string]string{req.Environment, req.Env} {
		if raw, ok := env["CLAWMANAGER_LLM_VISION"]; ok {
			return ParseVisionCapabilities(raw)
		}
	}
	return cfg.LLMVision, nil
}

func VisionInputModalities(existing any, enabled bool) []any {
	result := []any{}
	add := func(value any) {
		if s, ok := value.(string); ok && !strings.HasPrefix(s, "image") {
			result = append(result, s)
		}
	}
	switch values := existing.(type) {
	case []any:
		for _, v := range values {
			add(v)
		}
	case []string:
		for _, v := range values {
			add(v)
		}
	}
	if len(result) == 0 {
		result = append(result, "text")
	}
	if enabled {
		result = append(result, "image")
	}
	return result
}
