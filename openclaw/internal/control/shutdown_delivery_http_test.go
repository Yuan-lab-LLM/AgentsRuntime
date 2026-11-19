package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShutdownDeliveryHTTPRequiresExplicitSuccess(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"success":true}`, `{"version":1,"diagnostic_only":true,"accepted_ids":[]}`, `{"success":false,"data":{"version":1}}`, `{"success":true,"error":"secret","data":{}}`, `{"success":true,"data":null}`, strings.Repeat(" ", 65537) + `{}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Token") != "private-token" {
				t.Error("missing auth")
			}
			_, _ = w.Write([]byte(body))
		}))
		var data map[string]any
		err := shutdownDeliveryJSON(context.Background(), server.Client(), server.URL, "X-Test-Token", "private-token", "POST", map[string]any{"deliveries": []string{}}, &data)
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-token") {
			t.Fatal("invalid or unsanitized response")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":1,"diagnostic_only":true,"accepted_ids":[]}}`))
	}))
	defer server.Close()
	var data struct {
		Version int `json:"version"`
	}
	if shutdownDeliveryJSON(context.Background(), server.Client(), server.URL, "X-Test-Token", "token", "POST", nil, &data) != nil || data.Version != 1 {
		t.Fatal("valid envelope refused")
	}
}
func TestShutdownDeliveryHTTPNeverFollowsRedirects(t *testing.T) {
	hit := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true; w.WriteHeader(200) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer server.Close()
	var data any
	if shutdownDeliveryJSON(context.Background(), server.Client(), server.URL, "X-Test-Token", "private-token", "POST", nil, &data) == nil || hit {
		t.Fatal("redirect followed or accepted")
	}
}
