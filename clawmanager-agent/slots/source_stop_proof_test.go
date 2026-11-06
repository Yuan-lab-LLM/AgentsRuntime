package slots

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func sourceProofFixture(t *testing.T) (*Pool, map[string]any, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := gateway.Config{WorkspaceRoot: root, RuntimeType: "opencode", PodUID: "observer-pod", ControlToken: "control", GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
	p, err := NewPool(cfg, 1, func(context.Context, int, string, string, []byte) (int, []byte, error) {
		t.Error("proof must not inspect or forward current assignments")
		return 503, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".clawmanager-slot-locks")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, record := filepath.Join(dir, "opencode-1-155.lock"), filepath.Join(dir, "opencode-1-155.json")
	if err = os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = atomicJSON(record, writerRecord{InstanceID: 155, UserID: 1, Generation: 7, RuntimeType: "opencode", PodUID: "old-pod", Slot: "slot-0", State: "stopped"}); err != nil {
		t.Fatal(err)
	}
	return p, map[string]any{"instance_id": 155, "user_id": 1, "generation": 7, "runtime_type": "opencode", "pod_uid": "old-pod", "slot": "slot-0", "nonce": strings.Repeat("a", 64)}, lock, record
}

func sourceProofCall(p *Pool, body []byte, token, method string, secure bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/v1/source-writer/stopped-proof", bytes.NewReader(body))
	if secure {
		r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	}
	r.Header.Set(gateway.ControlTokenHeader, token)
	out := httptest.NewRecorder()
	p.ServeHTTP(out, r)
	return out
}

func TestSourceStoppedWriterProofReturnsExactExistingRecordWithoutWrites(t *testing.T) {
	p, req, lock, path := sourceProofFixture(t)
	before, _ := os.ReadFile(path)
	recordInfo, _ := os.Stat(path)
	lockInfo, _ := os.Stat(lock)
	body, _ := json.Marshal(req)
	out := sourceProofCall(p, body, "control", http.MethodPost, true)
	if out.Code != 200 {
		t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, value := range req {
		if got[key] != value && !(key == "instance_id" && got[key] == float64(155)) && !(key == "user_id" && got[key] == float64(1)) && !(key == "generation" && got[key] == float64(7)) {
			t.Errorf("%s=%v want %v", key, got[key], value)
		}
	}
	sum := sha256.Sum256(before)
	if got["observer_pod_uid"] != "observer-pod" || got["confirmed"] != true || got["source_record_digest"] != hex.EncodeToString(sum[:]) || len(got) != 10 {
		t.Fatalf("unexpected proof: %s", out.Body.String())
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	afterLock, _ := os.Stat(lock)
	entries, _ := os.ReadDir(filepath.Dir(path))
	if !bytes.Equal(before, after) || !os.SameFile(recordInfo, afterInfo) || !recordInfo.ModTime().Equal(afterInfo.ModTime()) || !os.SameFile(lockInfo, afterLock) || !lockInfo.ModTime().Equal(afterLock.ModTime()) || len(entries) != 2 {
		t.Fatal("proof changed existing writer state or created files")
	}
}

func TestSourceStoppedWriterProofRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name          string
		change        func(map[string]any)
		raw           string
		token, method string
		secure        bool
		status        int
	}{
		{name: "unauthenticated", method: "POST", secure: true, status: 401},
		{name: "wrong token", token: "bad", method: "POST", secure: true, status: 401},
		{name: "plaintext", token: "control", method: "POST", status: 403},
		{name: "wrong method", token: "control", method: "GET", secure: true, status: 405},
		{name: "unknown field", change: func(r map[string]any) { r["force"] = true }, status: 400},
		{name: "zero instance", change: func(r map[string]any) { r["instance_id"] = 0 }, status: 400},
		{name: "zero user", change: func(r map[string]any) { r["user_id"] = 0 }, status: 400},
		{name: "zero generation", change: func(r map[string]any) { r["generation"] = 0 }, status: 400},
		{name: "foreign runtime", change: func(r map[string]any) { r["runtime_type"] = "openclaw" }, status: 400},
		{name: "path runtime", change: func(r map[string]any) { r["runtime_type"] = "../opencode" }, status: 400},
		{name: "missing source pod", change: func(r map[string]any) { delete(r, "pod_uid") }, status: 400},
		{name: "same observer pod", change: func(r map[string]any) { r["pod_uid"] = "observer-pod" }, status: 400},
		{name: "noncanonical slot", change: func(r map[string]any) { r["slot"] = "slot-00" }, status: 400},
		{name: "invalid slot", change: func(r map[string]any) { r["slot"] = "slot-256" }, status: 400},
		{name: "missing nonce", change: func(r map[string]any) { delete(r, "nonce") }, status: 400},
		{name: "noncanonical nonce", change: func(r map[string]any) { r["nonce"] = strings.Repeat("A", 64) }, status: 400},
		{name: "noncanonical key", raw: `{"INSTANCE_ID":155,"user_id":1,"generation":7,"runtime_type":"opencode","pod_uid":"old-pod","slot":"slot-0","nonce":"` + strings.Repeat("a", 64) + `"}`, status: 400},
		{name: "null", raw: "null", status: 400},
		{name: "trailing json", raw: `{} {}`, status: 400},
		{name: "duplicate key", raw: `{"instance_id":155,"instance_id":155,"user_id":1,"generation":7,"runtime_type":"opencode","pod_uid":"old-pod","slot":"slot-0","nonce":"` + strings.Repeat("a", 64) + `"}`, status: 400},
		{name: "oversized", raw: strings.Repeat(" ", 4097), status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, req, _, _ := sourceProofFixture(t)
			if tc.change != nil {
				tc.change(req)
			}
			body, _ := json.Marshal(req)
			if tc.raw != "" {
				body = []byte(tc.raw)
			}
			token, method, secure := tc.token, tc.method, tc.secure
			if tc.method == "" {
				token, method, secure = "control", "POST", true
			}
			out := sourceProofCall(p, body, token, method, secure)
			if out.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", out.Code, tc.status, out.Body.String())
			}
		})
	}
}

func TestSourceStoppedWriterProofRejectsUnconfirmedOrUnsafeRecords(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *Pool, string, string)
	}{
		{"missing record", func(t *testing.T, p *Pool, l, r string) { os.Remove(r) }},
		{"missing lock", func(t *testing.T, p *Pool, l, r string) { os.Remove(l) }},
		{"missing directory", func(t *testing.T, p *Pool, l, r string) { os.RemoveAll(filepath.Dir(r)) }},
		{"missing root", func(t *testing.T, p *Pool, l, r string) {
			p.cfg.WorkspaceRoot = filepath.Join(p.cfg.WorkspaceRoot, "absent")
		}},
		{"writable root", func(t *testing.T, p *Pool, l, r string) { os.Chmod(p.cfg.WorkspaceRoot, 0777) }},
		{"writable directory", func(t *testing.T, p *Pool, l, r string) { os.Chmod(filepath.Dir(r), 0777) }},
		{"writable record", func(t *testing.T, p *Pool, l, r string) { os.Chmod(r, 0666) }},
		{"writable lock", func(t *testing.T, p *Pool, l, r string) { os.Chmod(l, 0666) }},
		{"record symlink", func(t *testing.T, p *Pool, l, r string) { os.Rename(r, r+".real"); os.Symlink(r+".real", r) }},
		{"lock symlink", func(t *testing.T, p *Pool, l, r string) { os.Rename(l, l+".real"); os.Symlink(l+".real", l) }},
		{"directory symlink", func(t *testing.T, p *Pool, l, r string) {
			dir := filepath.Dir(r)
			os.Rename(dir, dir+".real")
			os.Symlink(dir+".real", dir)
		}},
		{"root symlink", func(t *testing.T, p *Pool, l, r string) {
			alias := p.cfg.WorkspaceRoot + "-link"
			os.Symlink(p.cfg.WorkspaceRoot, alias)
			t.Cleanup(func() { os.Remove(alias) })
			p.cfg.WorkspaceRoot = alias
		}},
		{"held lock", func(t *testing.T, p *Pool, l, r string) {
			f, err := os.OpenFile(l, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			if err = lockWorkspace(f); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown record field", func(t *testing.T, p *Pool, l, r string) {
			data, _ := os.ReadFile(r)
			os.WriteFile(r, append(data[:len(data)-1], []byte(`,"force":true}`)...), 0600)
		}},
		{"trailing record", func(t *testing.T, p *Pool, l, r string) {
			data, _ := os.ReadFile(r)
			os.WriteFile(r, append(data, []byte(` {}`)...), 0600)
		}},
		{"duplicate record field", func(t *testing.T, p *Pool, l, r string) {
			data, _ := os.ReadFile(r)
			os.WriteFile(r, append(data[:len(data)-1], []byte(`,"instance_id":155}`)...), 0600)
		}},
		{"upgrading record", func(t *testing.T, p *Pool, l, r string) {
			data, _ := os.ReadFile(r)
			os.WriteFile(r, bytes.Replace(data, []byte(`"stopped"`), []byte(`"upgrading"`), 1), 0600)
		}},
		{"record directory", func(t *testing.T, p *Pool, l, r string) { os.Remove(r); os.Mkdir(r, 0700) }},
		{"oversized record", func(t *testing.T, p *Pool, l, r string) { os.WriteFile(r, []byte(strings.Repeat(" ", 4097)), 0600) }},
	}
	for _, field := range []string{"instance_id", "user_id", "generation", "runtime_type", "pod_uid", "slot", "state"} {
		field := field
		cases = append(cases, struct {
			name   string
			change func(*testing.T, *Pool, string, string)
		}{"mismatched " + field, func(t *testing.T, p *Pool, l, r string) {
			data, _ := os.ReadFile(r)
			var record map[string]any
			json.Unmarshal(data, &record)
			switch field {
			case "instance_id", "user_id", "generation":
				record[field] = 999
			case "state":
				record[field] = "running"
			default:
				record[field] = "foreign"
			}
			data, _ = json.Marshal(record)
			os.WriteFile(r, data, 0600)
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, req, lock, path := sourceProofFixture(t)
			tc.change(t, p, lock, path)
			body, _ := json.Marshal(req)
			_, recordErr := os.Stat(path)
			_, lockErr := os.Stat(lock)
			out := sourceProofCall(p, body, "control", "POST", true)
			if out.Code != 409 {
				t.Fatalf("status=%d want=409 body=%s", out.Code, out.Body.String())
			}
			for name, previous := range map[string]error{path: recordErr, lock: lockErr} {
				if _, err := os.Stat(name); os.IsNotExist(previous) && !os.IsNotExist(err) {
					t.Fatalf("proof created %s", name)
				}
			}
		})
	}
}

func TestSourceProofFileRejectsChangedIdentityAndMetadata(t *testing.T) {
	for _, change := range []string{"replace", "modify", "chmod", "remove", "symlink"} {
		t.Run(change, func(t *testing.T) {
			_, _, _, path := sourceProofFixture(t)
			f, before, err := openSourceProofFile(path, os.O_RDONLY)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			switch change {
			case "replace":
				data, _ := os.ReadFile(path)
				os.WriteFile(path+".new", data, 0600)
				os.Rename(path+".new", path)
			case "modify":
				os.WriteFile(path, []byte(`{}`), 0600)
			case "chmod":
				os.Chmod(path, 0644)
			case "remove":
				os.Remove(path)
			case "symlink":
				os.Rename(path, path+".old")
				os.Symlink(path+".old", path)
			}
			if err = sourceProofFileUnchanged(path, f, before); err == nil {
				t.Fatal("changed source accepted")
			}
		})
	}
}
