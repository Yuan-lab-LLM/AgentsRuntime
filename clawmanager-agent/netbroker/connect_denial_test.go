package netbroker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectRetainsPolicyDenialWithoutUpstreamDetails(t *testing.T) {
	for _, status := range []int{403, 401, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect || r.Host != "10.130.81.54:30444" {
					t.Error("unexpected CONNECT target")
				}
				w.Header().Set("X-Private-Diagnostic", "must-not-disclose")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("must-not-disclose"))
			}))
			defer upstream.Close()
			b, err := NewFixedEgress(context.Background(), "127.0.0.1", upstream.URL, "bw1.fake-only")
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			r := httptest.NewRequest(http.MethodConnect, "http://10.130.81.54:30444", nil)
			r.Host, r.RemoteAddr = "10.130.81.54:30444", "127.0.0.1:30000"
			w := httptest.NewRecorder()
			b.Egress().ServeHTTP(w, r)
			want := 502
			if status == 403 {
				want = 403
			}
			if w.Code != want || w.Header().Get("X-Private-Diagnostic") != "" || strings.Contains(w.Body.String(), "must-not-disclose") {
				t.Fatalf("unsafe denial response: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
