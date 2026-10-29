// Package slots coordinates fixed, independently isolated runtime containers.
package slots

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type Call func(context.Context, int, string, string, []byte) (int, []byte, error)

type Assignment struct {
	GatewayID  string `json:"gateway_id"`
	InstanceID int    `json:"instance_id"`
	UserID     int    `json:"user_id"`
	Generation int    `json:"generation"`
	State      string `json:"state"`
}

type Snapshot struct {
	Gateways    []gateway.GatewayState     `json:"gateways"`
	Assignment  *Assignment                `json:"assignment,omitempty"`
	Skills      gateway.SkillReportPayload `json:"skills"`
	Quarantined bool                       `json:"quarantined"`
}

type Pool struct {
	cfg      gateway.Config
	count    int
	call     Call
	metadata *gateway.GatewayManager
	mu       sync.RWMutex
	proxyMu  sync.Mutex
	draining bool
}

func NewPool(cfg gateway.Config, count int, call Call) (*Pool, error) {
	if count < 1 || count > 256 || cfg.ControlToken == "" || call == nil {
		return nil, errors.New("slot pool requires a token, transport and 1..256 slots")
	}
	if cfg.GatewayPortBlockSize < 1 || cfg.GatewayPortStart < 1 || cfg.GatewayPortStart+count*cfg.GatewayPortBlockSize-1 > cfg.GatewayPortEnd || cfg.GatewayPortEnd > 65535 {
		return nil, errors.New("slot pool port range is insufficient")
	}
	return &Pool{cfg: cfg, count: count, call: call, metadata: gateway.NewGatewayManager(cfg, nil, nil)}, nil
}

func (p *Pool) snapshots(ctx context.Context) ([]Snapshot, int) {
	snapshots := make([]Snapshot, p.count)
	healthy := 0
	var wg sync.WaitGroup
	for i := range snapshots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, data, err := p.call(ctx, i, http.MethodGet, "/v1/slot-state", nil)
			if err != nil || status != 200 || json.Unmarshal(data, &snapshots[i]) != nil {
				snapshots[i].Quarantined = true
				return
			}
		}(i)
	}
	wg.Wait()
	for _, s := range snapshots {
		if !s.Quarantined {
			healthy++
		}
	}
	return snapshots, healthy
}

func (p *Pool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/readyz" {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		if !p.metadata.ReadyForTraffic() {
			http.Error(w, "upgrade activation pending", 503)
			return
		}
		if _, healthy := p.snapshots(r.Context()); healthy == 0 {
			http.Error(w, "no healthy slots", 503)
			return
		}
		w.WriteHeader(200)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(gateway.ControlTokenHeader)), []byte(p.cfg.ControlToken)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.URL.Path == "/v1/health" && r.Method == http.MethodGet {
		if _, healthy := p.snapshots(r.Context()); healthy == 0 {
			http.Error(w, "no healthy slots", 503)
			return
		}
		status := "ready"
		p.mu.RLock()
		if p.draining {
			status = "draining"
		}
		p.mu.RUnlock()
		metadata := p.metadata
		if metadata.UpgradeStandby() {
			status = "standby"
		}
		writeJSON(w, 200, gateway.HealthResponse{Status: status, Capabilities: metadata.HealthCapabilities()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "invalid request body", 400)
		return
	}
	if r.URL.Path == "/v1/drain" && r.Method == http.MethodPost {
		var req struct {
			Draining bool `json:"draining"`
		}
		if json.Unmarshal(body, &req) != nil {
			http.Error(w, "invalid drain config", 400)
			return
		}
		// Block new creates before asking peers to drain, including on a partial failure.
		p.mu.Lock()
		p.draining = true
		p.mu.Unlock()
		if !p.broadcast(w, r, body) {
			return
		}
		p.mu.Lock()
		p.draining = req.Draining
		p.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"draining": req.Draining})
		return
	}
	if r.URL.Path == "/v1/proxy-policy" {
		if r.Method == http.MethodGet {
			p.forward(w, r, 0, body)
		} else if r.Method == http.MethodPut {
			p.updateProxyPolicy(w, r, body)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if r.URL.Path == "/v1/openclaw/upgrade/activate" {
		if r.Method == http.MethodGet {
			p.forward(w, r, 0, body)
			return
		}
		if p.broadcast(w, r, body) {
			if r.URL.Path == "/v1/openclaw/upgrade/activate" {
				var req struct {
					RolloutID string `json:"rollout_id"`
				}
				if json.Unmarshal(body, &req) != nil || p.metadata.ActivateUpgrade(req.RolloutID) != nil {
					http.Error(w, "pool upgrade activation failed", 409)
					return
				}
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
		return
	}
	if r.URL.Path == "/v1/gateways/cancel-pending" && r.Method == http.MethodPost {
		req, err := decodePendingCancellation(body, p.cfg)
		offset := req.GatewayPort - p.cfg.GatewayPortStart
		if err != nil || offset < 0 || offset%p.cfg.GatewayPortBlockSize != 0 || offset/p.cfg.GatewayPortBlockSize >= p.count {
			http.Error(w, "invalid cancellation scope or slot block", 400)
			return
		}
		p.forward(w, r, offset/p.cfg.GatewayPortBlockSize, body)
		return
	}
	if r.URL.Path == "/v1/gateways" && r.Method == http.MethodPost {
		p.mu.RLock()
		draining := p.draining
		p.mu.RUnlock()
		if draining {
			http.Error(w, "runtime pod is draining", 409)
			return
		}
		var req gateway.CreateGatewayRequest
		if json.Unmarshal(body, &req) != nil || req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.AgentType != p.cfg.RuntimeType || req.PodUID != p.cfg.PodUID {
			http.Error(w, "invalid gateway identity", 400)
			return
		}
		offset := req.GatewayPort - p.cfg.GatewayPortStart
		if offset < 0 || offset%p.cfg.GatewayPortBlockSize != 0 || offset/p.cfg.GatewayPortBlockSize >= p.count {
			http.Error(w, "gateway port must identify an exact slot block", 400)
			return
		}
		index := offset / p.cfg.GatewayPortBlockSize
		req.PortRange = gateway.PortRange{Start: req.GatewayPort, End: req.GatewayPort + p.cfg.GatewayPortBlockSize - 1}
		body, _ = json.Marshal(req)
		p.forward(w, r, index, body)
		return
	}
	snapshots, _ := p.snapshots(r.Context())
	if strings.HasPrefix(r.URL.Path, "/v1/gateways/") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/gateways/"), "/stop")
		for i, s := range snapshots {
			if s.Assignment != nil && s.Assignment.GatewayID == id {
				p.forward(w, r, i, body)
				return
			}
			for _, g := range s.Gateways {
				if g.GatewayID == id {
					p.forward(w, r, i, body)
					return
				}
			}
		}
		if r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/stop") {
			http.Error(w, "gateway stop is not confirmed by any slot", 409)
		} else {
			http.Error(w, "gateway not found", 404)
		}
		return
	}
	var identity struct {
		InstanceID int `json:"instance_id"`
	}
	if json.Unmarshal(body, &identity) != nil || identity.InstanceID < 1 {
		http.Error(w, "instance identity is required", 400)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/openclaw/") && r.URL.Path != "/v1/skills/resync" {
		http.NotFound(w, r)
		return
	}
	active := -1
	for i, s := range snapshots {
		if s.Assignment != nil && s.Assignment.InstanceID == identity.InstanceID && s.Assignment.State != "stopped" {
			if active != -1 {
				http.Error(w, "multiple writer assignments require reconciliation", 409)
				return
			}
			active = i
		}
	}
	if active >= 0 {
		p.forward(w, r, active, body)
		return
	}
	for i, s := range snapshots {
		if s.Assignment != nil && s.Assignment.InstanceID == identity.InstanceID {
			p.forward(w, r, i, body)
			return
		}
	}
	// Upgrade work is guarded by the existing shared-workspace lease protocol;
	// keep a stable peer for instances which have not yet started on this Pod.
	p.forward(w, r, identity.InstanceID%p.count, body)
}

// Slot revisions are independent CAS tokens. The caller compares against slot0,
// while each peer update compares against that peer's own current revision.
func (p *Pool) updateProxyPolicy(w http.ResponseWriter, r *http.Request, body []byte) {
	p.proxyMu.Lock()
	defer p.proxyMu.Unlock()
	var req gateway.ProxyPolicyUpdate
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if len(body) > 16384 || decoder.Decode(&req) != nil || req.Namespace == "" || req.Namespace != p.cfg.Namespace || len(req.Proxies) == 0 || len(req.Proxies) > 128 {
		http.Error(w, "invalid proxy policy", 400)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		http.Error(w, "invalid proxy policy", 400)
		return
	}
	unique := map[string]bool{}
	for _, raw := range req.Proxies {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" || !ip.IsGlobalUnicast() {
			http.Error(w, "invalid proxy addresses", 400)
			return
		}
		unique[ip.Unmap().String()] = true
	}
	desired := make([]string, 0, len(unique))
	for ip := range unique {
		desired = append(desired, ip)
	}
	sort.Strings(desired)
	req.Proxies = desired
	read := func(index int) (gateway.ProxyPolicy, bool) {
		status, data, err := p.call(r.Context(), index, http.MethodGet, r.URL.RequestURI(), nil)
		var policy gateway.ProxyPolicy
		if err != nil || status != 200 || json.Unmarshal(data, &policy) != nil || policy.Contract != 1 || policy.Namespace != req.Namespace {
			http.Error(w, "slot "+strconv.Itoa(index)+" proxy policy unavailable", 503)
			return policy, false
		}
		return policy, true
	}
	current, ok := read(0)
	if !ok {
		return
	}
	if current.Revision != req.ExpectedRevision {
		http.Error(w, gateway.ErrProxyPolicyConflict.Error(), 409)
		return
	}
	validAck := func(policy gateway.ProxyPolicy) bool {
		now := time.Now()
		return policy.Contract == 1 && policy.Namespace == req.Namespace && policy.Revision != "" && slices.Equal(policy.Proxies, desired) && policy.ExpiresAtMS > now.Add(10*time.Second).UnixMilli() && policy.ExpiresAtMS <= now.Add(90*time.Second).UnixMilli()
	}
	var first gateway.ProxyPolicy
	for i := 0; i < p.count; i++ {
		if i > 0 {
			current, ok = read(i)
			if !ok {
				return
			}
		}
		req.ExpectedRevision = current.Revision
		peerBody, _ := json.Marshal(req)
		status, data, err := p.call(r.Context(), i, http.MethodPut, r.URL.RequestURI(), peerBody)
		var ack gateway.ProxyPolicy
		if err != nil || status != 200 || json.Unmarshal(data, &ack) != nil || !validAck(ack) {
			http.Error(w, "slot "+strconv.Itoa(i)+" did not acknowledge proxy policy", 503)
			return
		}
		if i == 0 {
			first = ack
		}
	}
	if !validAck(first) {
		http.Error(w, "pool proxy policy acknowledgement expired", 503)
		return
	}
	writeJSON(w, 200, first)
}

func (p *Pool) forward(w http.ResponseWriter, r *http.Request, index int, body []byte) {
	status, data, err := p.call(r.Context(), index, r.Method, r.URL.RequestURI(), body)
	if err != nil {
		http.Error(w, "slot unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func (p *Pool) broadcast(w http.ResponseWriter, r *http.Request, body []byte) bool {
	for i := 0; i < p.count; i++ {
		status, _, err := p.call(r.Context(), i, r.Method, r.URL.RequestURI(), body)
		if err != nil || status < 200 || status >= 300 {
			http.Error(w, "slot "+strconv.Itoa(i)+" did not acknowledge control operation", 503)
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func slotIndex(raw string) (int, error) {
	i, err := strconv.Atoi(raw)
	if err != nil || i < 0 || i >= 256 {
		return 0, fmt.Errorf("invalid slot index")
	}
	return i, nil
}
