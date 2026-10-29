package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestManagedStartupPolicyBlocksBeforeProcessOrWorkspace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "policy.json")
	cfg := Config{RuntimeType: "openclaw", Namespace: "tenant", ProxyPolicyFile: file}
	starter := NewExecProcessStarter(cfg)
	if _, err := starter.StartGateway(context.Background(), GatewayStartSpec{WorkspacePath: filepath.Join(dir, "untouched")}); !errors.Is(err, ErrProxyPolicyNotReady) {
		t.Fatalf("missing policy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "untouched")); !os.IsNotExist(err) {
		t.Fatal("startup wrote workspace before trust validation")
	}
	store := NewProxyPolicyStore(file, "tenant")
	p, err := store.Update(ProxyPolicyUpdate{Namespace: "tenant", Proxies: []string{"10.1.2.3"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProxyPolicyForStart(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ProxyPolicy){
		func(p *ProxyPolicy) { p.Namespace = "other" },
		func(p *ProxyPolicy) { p.Proxies = []string{"10.0.0.0/8"} },
		func(p *ProxyPolicy) { p.ExpiresAtMS = time.Now().Add(-time.Second).UnixMilli() },
	} {
		bad := p
		change(&bad)
		raw, _ := json.Marshal(bad)
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(validateProxyPolicyForStart(cfg), ErrProxyPolicyNotReady) {
			t.Fatal("invalid startup policy accepted")
		}
	}
	if err := validateProxyPolicyForStart(Config{RuntimeType: "hermes", Namespace: "tenant", ProxyPolicyFile: file}); !errors.Is(err, ErrProxyPolicyNotReady) {
		t.Fatal("managed Hermes accepted expired policy", err)
	}
	if err := validateProxyPolicyForStart(Config{RuntimeType: "hermes"}); err != nil {
		t.Fatal("changed legacy Hermes", err)
	}
	if err := validateProxyPolicyForStart(Config{RuntimeType: "openclaw"}); err != nil {
		t.Fatal("changed legacy", err)
	}
}

func TestProxyPolicyCASNamespaceAndExactAddresses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	s := NewProxyPolicyStore(file, "tenant-a")
	p, err := s.Update(ProxyPolicyUpdate{Namespace: "tenant-a", Proxies: []string{"10.1.1.2", "2001:db8::2", "10.1.1.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Proxies) != 2 || p.ExpiresAtMS <= time.Now().UnixMilli() || p.ExpiresAtMS > time.Now().Add(61*time.Second).UnixMilli() {
		t.Fatal(p)
	}
	for _, req := range []ProxyPolicyUpdate{
		{Namespace: "tenant-a", Proxies: []string{"10.1.1.3"}},
		{Namespace: "tenant-b", ExpectedRevision: p.Revision, Proxies: []string{"10.1.1.3"}},
		{Namespace: "tenant-a", ExpectedRevision: p.Revision, Proxies: []string{"10.0.0.0/8"}},
		{Namespace: "tenant-a", ExpectedRevision: p.Revision, Proxies: []string{"0.0.0.0"}},
	} {
		if _, err := s.Update(req); err == nil {
			t.Fatalf("accepted invalid update: %+v", req)
		}
	}
	if !reflect.DeepEqual(s.Snapshot(), p) {
		t.Fatal("failed update replaced valid policy")
	}
	if _, err := s.Update(ProxyPolicyUpdate{Namespace: "tenant-a", Proxies: []string{"10.1.1.3"}}); !errors.Is(err, ErrProxyPolicyConflict) {
		t.Fatal(err)
	}
	q, err := s.Update(ProxyPolicyUpdate{Namespace: "tenant-a", ExpectedRevision: p.Revision, Proxies: []string{"10.1.1.3"}})
	if err != nil {
		t.Fatal(err)
	}
	var stored ProxyPolicy
	raw, _ := os.ReadFile(file)
	if err := json.Unmarshal(raw, &stored); err != nil || !reflect.DeepEqual(q, stored) {
		t.Fatalf("stored policy mismatch: %v", err)
	}
}
