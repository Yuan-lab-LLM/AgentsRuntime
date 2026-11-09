package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const ManagedProxyPolicyFile = "/run/clawmanager-proxy-policy/policy.json"

var ErrProxyPolicyConflict = errors.New("proxy policy revision conflict")
var ErrProxyPolicyNotReady = errors.New("PROXY_POLICY_NOT_READY: managed proxy policy is not ready")

// Covers first start and process-supervisor restarts without touching config.
func validateProxyPolicyForStart(cfg Config) error {
	if cfg.ProxyPolicyFile == "" {
		return nil
	}
	raw, err := os.ReadFile(cfg.ProxyPolicyFile)
	if err != nil || len(raw) > 16384 {
		return ErrProxyPolicyNotReady
	}
	var policy ProxyPolicy
	now := time.Now().UnixMilli()
	if json.Unmarshal(raw, &policy) != nil || policy.Contract != 1 || policy.Namespace != cfg.Namespace ||
		policy.ExpiresAtMS <= now || policy.ExpiresAtMS > now+90000 || len(policy.Proxies) == 0 || len(policy.Proxies) > 128 {
		return ErrProxyPolicyNotReady
	}
	for _, value := range policy.Proxies {
		ip, err := netip.ParseAddr(value)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
			return ErrProxyPolicyNotReady
		}
	}
	return nil
}

type ProxyPolicy struct {
	Contract    int      `json:"contract"`
	Revision    string   `json:"revision"`
	Namespace   string   `json:"namespace"`
	Proxies     []string `json:"proxies"`
	ExpiresAtMS int64    `json:"expires_at_ms"`
}

type ProxyPolicyUpdate struct {
	ExpectedRevision string   `json:"expected_revision"`
	Namespace        string   `json:"namespace"`
	Proxies          []string `json:"proxies"`
}

// The file is outside all member workspaces. Request environments cannot
// override its location. Gateways read this policy without touching config,
// changing generation, restarting writers or running migration hooks.
type ProxyPolicyStore struct {
	mu              sync.Mutex
	file, namespace string
	current         ProxyPolicy
}

func NewProxyPolicyStore(file, namespace string) *ProxyPolicyStore {
	return &ProxyPolicyStore{file: file, namespace: namespace, current: ProxyPolicy{Contract: 1, Namespace: namespace}}
}

func (s *ProxyPolicyStore) Snapshot() ProxyPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.current
	result.Proxies = append([]string(nil), result.Proxies...)
	return result
}

func (s *ProxyPolicyStore) Update(req ProxyPolicyUpdate) (ProxyPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Namespace != s.namespace || s.namespace == "" {
		return ProxyPolicy{}, fmt.Errorf("proxy policy namespace mismatch")
	}
	if req.ExpectedRevision != s.current.Revision {
		return ProxyPolicy{}, ErrProxyPolicyConflict
	}
	if len(req.Proxies) == 0 || len(req.Proxies) > 128 {
		return ProxyPolicy{}, fmt.Errorf("invalid proxy count")
	}
	unique := map[string]bool{}
	for _, raw := range req.Proxies {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
			return ProxyPolicy{}, fmt.Errorf("proxy policy requires exact unicast IP addresses")
		}
		unique[ip.Unmap().String()] = true
	}
	proxies := make([]string, 0, len(unique))
	for ip := range unique {
		proxies = append(proxies, ip)
	}
	sort.Strings(proxies)
	var revision [16]byte
	if _, err := rand.Read(revision[:]); err != nil {
		return ProxyPolicy{}, err
	}
	next := ProxyPolicy{Contract: 1, Namespace: s.namespace, Revision: hex.EncodeToString(revision[:]), Proxies: proxies, ExpiresAtMS: time.Now().Add(60 * time.Second).UnixMilli()}
	raw, err := json.Marshal(next)
	if err != nil {
		return ProxyPolicy{}, err
	}
	if err := ensureProxyPolicyDirectory(filepath.Dir(s.file)); err != nil {
		return ProxyPolicy{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(s.file), ".policy-")
	if err != nil {
		return ProxyPolicy{}, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0644); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return ProxyPolicy{}, err
	}
	if closeErr != nil {
		return ProxyPolicy{}, closeErr
	}
	if err := os.Rename(f.Name(), s.file); err != nil {
		return ProxyPolicy{}, err
	}
	s.current = next
	result := next
	result.Proxies = append([]string(nil), next.Proxies...)
	return result, nil
}
