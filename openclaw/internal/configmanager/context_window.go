package configmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func readManagedContextWindows() (int, map[string]int, error) {
	fallback := 0
	if raw, ok := os.LookupEnv("CLAWMANAGER_LLM_CONTEXT_WINDOW"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value < 0 || int64(value) > 2147483647 {
			return 0, nil, fmt.Errorf("invalid managed context window")
		}
		fallback = value
	}
	var windows map[string]int
	if raw, ok := os.LookupEnv("CLAWMANAGER_LLM_CONTEXT_WINDOWS"); ok {
		if err := json.Unmarshal([]byte(raw), &windows); err != nil || windows == nil {
			return 0, nil, fmt.Errorf("invalid managed context catalog")
		}
		for id, value := range windows {
			if strings.TrimSpace(id) == "" || value < 0 || int64(value) > 2147483647 {
				return 0, nil, fmt.Errorf("invalid model context window")
			}
		}
	}
	return fallback, windows, nil
}

func (o llmOverrides) contextFor(provider, model string) int {
	if value, ok := o.ContextWindows[provider+"/"+model]; ok {
		return value
	}
	if value, ok := o.ContextWindows[model]; ok {
		return value
	}
	return o.ContextWindow
}
