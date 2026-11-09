package dshproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseLaunchURL(t *testing.T) {
	launchURL, matched, err := parseLaunchURL("dsh web: http://127.0.0.1:32145/?token=launch-secret (LAN: http://10.0.0.2:32145/?token=launch-secret)")
	if err != nil {
		t.Fatalf("parseLaunchURL() error = %v", err)
	}
	if !matched {
		t.Fatal("parseLaunchURL() matched = false, want true")
	}
	if got := launchURL.Query().Get("token"); got != "launch-secret" {
		t.Fatalf("launch token = %q, want launch-secret", got)
	}

	if _, matched, err := parseLaunchURL("ordinary DSH output"); err != nil || matched {
		t.Fatalf("ordinary output = matched %v, error %v", matched, err)
	}
	if _, matched, err := parseLaunchURL("dsh web: http://example.com/?token=secret"); !matched || err == nil {
		t.Fatalf("non-loopback URL = matched %v, error %v; want matched error", matched, err)
	}
}

func TestScanDSHStdoutDoesNotLogLaunchToken(t *testing.T) {
	const launchToken = "do-not-log-this-launch-token"
	var output strings.Builder
	launchURLs := make(chan *url.URL, 1)
	scanErrors := make(chan error, 1)
	scanDSHStdout(
		strings.NewReader("dsh web: http://127.0.0.1:32145/?token="+launchToken+"\n"),
		&output,
		launchURLs,
		scanErrors,
	)
	if strings.Contains(output.String(), launchToken) || strings.Contains(output.String(), "token=") {
		t.Fatalf("startup output leaked DSH launch token: %q", output.String())
	}
	select {
	case launchURL := <-launchURLs:
		if got := launchURL.Query().Get("token"); got != launchToken {
			t.Fatalf("parsed launch token = %q", got)
		}
	default:
		t.Fatal("scanDSHStdout() did not publish launch URL")
	}
}

func TestBootstrapBrowserSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" || request.URL.Query().Get("token") != "launch-secret" {
			t.Fatalf("bootstrap request URL = %s", request.URL.String())
		}
		writer.Header().Set("Set-Cookie", "dsh-auth-test=session-secret; Path=/; HttpOnly; SameSite=Strict")
		writer.Header().Set("Location", "/")
		writer.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()

	launchURL, err := url.Parse(server.URL + "/?token=launch-secret")
	if err != nil {
		t.Fatal(err)
	}
	target, cookie, err := bootstrapBrowserSession(context.Background(), launchURL)
	if err != nil {
		t.Fatalf("bootstrapBrowserSession() error = %v", err)
	}
	if target.String() != server.URL {
		t.Fatalf("target = %q, want %q", target.String(), server.URL)
	}
	if cookie != "dsh-auth-test=session-secret" {
		t.Fatalf("cookie = %q", cookie)
	}
}

func TestAuthenticatedProxyKeepsDSHSessionInternal(t *testing.T) {
	var upstreamRequests atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamRequests.Add(1)
		if request.Host != strings.TrimPrefix(upstream.URL, "http://") {
			t.Errorf("upstream Host = %q", request.Host)
		}
		if got := request.Header.Get("Origin"); got != upstream.URL {
			t.Errorf("upstream Origin = %q, want %q", got, upstream.URL)
		}
		for _, name := range []string{"Authorization", "X-Api-Key", instanceTokenHeader} {
			if got := request.Header.Get(name); got != "" {
				t.Errorf("upstream %s = %q, want empty", name, got)
			}
		}
		cookie := request.Header.Get("Cookie")
		if !strings.Contains(cookie, "theme=dark") || !strings.Contains(cookie, "dsh-auth-current=session-secret") {
			t.Errorf("upstream Cookie = %q", cookie)
		}
		if strings.Contains(cookie, "dsh-auth-old=stale") {
			t.Errorf("upstream Cookie retained stale DSH session: %q", cookie)
		}
		writer.Header().Add("Set-Cookie", "dsh-auth-current=rotated; Path=/")
		writer.Header().Add("Set-Cookie", "plugin-preference=compact; Path=/")
		_, _ = io.WriteString(writer, "ok")
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(newAuthenticatedProxy(target, "instance-secret", "dsh-auth-current=session-secret"))
	defer proxy.Close()

	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "wrong-secret", status: http.StatusUnauthorized},
		{name: "managed", token: "instance-secret", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodGet, proxy.URL+"/", nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if test.token != "" {
				request.Header.Set(instanceTokenHeader, test.token)
				request.Header.Set("Authorization", "Bearer "+test.token)
				request.Header.Set("X-Api-Key", test.token)
			}
			request.Header.Set("Origin", "https://instance.example.test")
			request.Header.Set("Cookie", "theme=dark; dsh-auth-old=stale")
			response, responseErr := http.DefaultClient.Do(request)
			if responseErr != nil {
				t.Fatal(responseErr)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if test.status == http.StatusOK {
				for _, cookie := range response.Header.Values("Set-Cookie") {
					if strings.HasPrefix(cookie, dshCookiePrefix) {
						t.Fatalf("response leaked internal DSH cookie: %q", cookie)
					}
				}
				if got := response.Header.Get("Set-Cookie"); !strings.HasPrefix(got, "plugin-preference=") {
					t.Fatalf("plugin Set-Cookie = %q", got)
				}
			}
		})
	}
	if got := upstreamRequests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestAuthenticatedProxyRestoresClawManagerEncodedPluginBatchQuery(t *testing.T) {
	const wantRawQuery = "?@deepseek-ai/dsh-client-modules/client.js&rev=cddf5581d5d5"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/plugins/" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		if request.URL.RawQuery != wantRawQuery {
			t.Fatalf("upstream raw query = %q, want %q", request.URL.RawQuery, wantRawQuery)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(newAuthenticatedProxy(target, "instance-secret", "dsh-auth-current=session-secret"))
	defer proxy.Close()
	requestURL := proxy.URL + "/plugins/?%3F%40deepseek-ai%2Fdsh-client-modules%2Fclient.js=&rev=cddf5581d5d5"
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(instanceTokenHeader, "instance-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}
