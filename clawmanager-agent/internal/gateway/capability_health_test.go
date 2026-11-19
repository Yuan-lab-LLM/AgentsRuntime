package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const healthyCapabilityJSON = `{"protocol":1,"generation":7,"enrolled":true,"enrollment_generation":7,"registration_is_sticky":true,"current_report":{"runtime_version":"2026.10.2","adapter_version":"cm-openclaw-native-1"}}`

func TestOpenClawCapabilityHealth(t *testing.T) {
	body := healthyCapabilityJSON
	status := 200
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/runtime/capabilities" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect request")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	env := []string{taskProtocolFlag + "=1", renewalFlag + "=1", "CLAWMANAGER_LLM_BASE_URL=" + server.URL, "CLAWMANAGER_LLM_API_KEY=test-key", "CLAWMANAGER_GATEWAY_GENERATION=7"}
	if err := checkCapabilityHealth(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{}", "null", healthyCapabilityJSON + "{}", strings.Repeat("x", 4097),
		strings.Replace(healthyCapabilityJSON, `"generation":7`, `"generation":6`, 1),
		strings.Replace(healthyCapabilityJSON, `"enrollment_generation":7`, `"enrollment_generation":6`, 1),
		strings.Replace(healthyCapabilityJSON, `"enrolled":true`, `"enrolled":false`, 1),
		strings.Replace(healthyCapabilityJSON, `"current_report":{`, `"other_report":{`, 1),
		strings.Replace(healthyCapabilityJSON, "2026.10.2", "old", 1),
		strings.Replace(healthyCapabilityJSON, "cm-openclaw-native-1", "old", 1),
		strings.Replace(healthyCapabilityJSON, `"registration_is_sticky":true`, `"registration_is_sticky":false`, 1),
	} {
		body = bad
		if checkCapabilityHealth(context.Background(), env) == nil {
			t.Fatal("invalid report passed")
		}
	}
	body = healthyCapabilityJSON
	for _, code := range []int{401, 403, 404, 409, 500} {
		status = code
		if checkCapabilityHealth(context.Background(), env) == nil {
			t.Fatal("HTTP failure passed")
		}
	}
	before := calls
	if checkCapabilityHealth(context.Background(), nil) != nil || calls != before {
		t.Fatal("legacy made a request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checkCapabilityHealth(ctx, env) == nil {
		t.Fatal("cancelled probe passed")
	}
}
func TestOpenClawCapabilityHealthRejectsRedirect(t *testing.T) {
	leaks := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks++; w.Write([]byte(healthyCapabilityJSON)) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer server.Close()
	env := []string{taskProtocolFlag + "=1", renewalFlag + "=1", "CLAWMANAGER_LLM_BASE_URL=" + server.URL, "CLAWMANAGER_LLM_API_KEY=test-key", "CLAWMANAGER_GATEWAY_GENERATION=7"}
	if checkCapabilityHealth(context.Background(), env) == nil || leaks != 0 {
		t.Fatal("redirect leaked credential or passed")
	}
}
