package netbroker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rejectedBody struct {
	io.ReadCloser
	onClose  func()
	observed atomic.Bool
}

func (b *rejectedBody) Close() error {
	if b.observed.CompareAndSwap(false, true) {
		b.onClose()
	}
	return b.ReadCloser.Close()
}

func TestRejectedPartialBodiesCloseBeforeEscapingAccounting(t *testing.T) {
	for _, scenario := range []string{"model-route", "model-method", "egress-target", "wrong-source", "busy", "closing", "shutdown-during-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			var forwarded atomic.Int32
			backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) })
			creds, err := NewCredentials(context.Background(), backend.URL, "control-sentinel", testIdentity)
			if err != nil {
				t.Fatal(err)
			}
			defer creds.Close()
			child := "127.0.0.1"
			if scenario == "wrong-source" {
				child = "127.0.0.2"
			}
			b, err := New(context.Background(), Config{ChildIP: child, BackendURL: backend.URL, EgressProxyURL: backend.URL, Credentials: creds})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			// Fill real admission slots without opening hundreds of unrelated sockets;
			// the over-capacity request itself uses a real TCP connection below.
			var release []func()
			if scenario == "busy" {
				for range 256 {
					_, finish, ok := b.enter(httptest.NewRecorder(), localRequest("GET", modelPrefix+"/models"))
					if !ok {
						t.Fatal("could not fill admission budget")
					}
					release = append(release, finish)
				}
			}
			defer func() {
				for _, finish := range release {
					finish()
				}
			}()
			if scenario == "closing" {
				b.Close()
			}
			handler := b.Model()
			method, target, want := "POST", modelPrefix+"/models", 400
			switch scenario {
			case "model-route", "shutdown-during-cleanup":
				target = "/outside"
			case "model-method":
				method = "PUT"
				want = 405
			case "egress-target":
				handler = b.Egress()
				target = "/relative"
			case "wrong-source":
				want = 403
			case "busy", "closing":
				want = 503
			}
			closed, returned, bodyClosed := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body = &rejectedBody{ReadCloser: r.Body, onClose: func() {
					if scenario == "model-route" || scenario == "model-method" || scenario == "egress-target" || scenario == "shutdown-during-cleanup" {
						if len(b.limit) != 1 {
							t.Error("body cleanup ran after admission was released")
						}
					}
					close(bodyClosed)
				}}
				handler.ServeHTTP(w, r)
				close(returned)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			server.Start()
			defer server.Close()
			client, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err = fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: local\r\nContent-Length: 8\r\nX-Forwarded-For: 127.0.0.1\r\n\r\nx", method, target); err != nil {
				t.Fatal(err)
			}
			var shutdownJoined chan struct{}
			if scenario == "shutdown-during-cleanup" {
				select {
				case <-bodyClosed:
				case <-time.After(time.Second):
					t.Fatal("rejection did not begin body cleanup")
				}
				shutdownJoined = make(chan struct{})
				go func() { b.Close(); close(shutdownJoined) }()
			}
			// No close or remaining body bytes from this client may release the server.
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("rejected silent upload retained a server connection after broker accounting")
			}
			select {
			case <-returned:
			default:
				t.Fatal("closed rejection handler retained")
			}
			select {
			case <-bodyClosed:
			default:
				t.Fatal("request body was left for unaccounted post-handler draining")
			}
			response, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err == nil {
				defer response.Body.Close()
				payload, _ := io.ReadAll(response.Body)
				if response.StatusCode != want || strings.Contains(string(payload), "sentinel") {
					t.Fatalf("unsafe rejection response: status=%d body=%s", response.StatusCode, payload)
				}
			}
			if shutdownJoined != nil {
				select {
				case <-shutdownJoined:
				case <-time.After(time.Second):
					t.Fatal("shutdown did not join admitted rejection cleanup")
				}
			}
			if forwarded.Load() != 0 {
				t.Fatalf("rejected traffic forwarded %d times", forwarded.Load())
			}
			expected := 0
			if scenario == "busy" {
				expected = 256
			}
			if len(b.limit) != expected {
				t.Fatalf("admission budget leaked: %d want %d", len(b.limit), expected)
			}
		})
	}
}

func TestAdmittedSlowUploadOutlivesRejectionCleanup(t *testing.T) {
	for _, model := range []bool{false, true} {
		t.Run(fmt.Sprint("model=", model), func(t *testing.T) {
			arrived := make(chan struct{})
			backend := brokerBackend(t, func(w http.ResponseWriter, r *http.Request) {
				close(arrived)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "ab" {
					t.Errorf("legitimate slow body lost: %q %v", body, err)
					return
				}
				fmt.Fprint(w, "accepted")
			})
			b := brokerForTest(t, backend.URL, backend.URL)
			handler := b.Egress()
			target := "http://origin.invalid/upload"
			if model {
				handler = b.Model()
				target = modelPrefix + "/chat/completions"
			}
			server := httptest.NewServer(handler)
			defer func() { b.Close(); server.Close() }()
			client, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(client, "POST %s HTTP/1.1\r\nHost: local\r\nContent-Length: 2\r\n\r\na", target)
			select {
			case <-arrived:
			case <-time.After(time.Second):
				t.Fatal("slow upload never forwarded")
			}
			time.Sleep(3 * rejectionCleanupTimeout)
			if len(b.limit) != 1 {
				t.Fatal("legitimate upload lost admission while body remained open")
			}
			if _, err = io.WriteString(client, "b"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 200 || string(body) != "accepted" {
				t.Fatalf("valid slow upload failed: status=%d body=%s err=%v", response.StatusCode, body, err)
			}
		})
	}
}
