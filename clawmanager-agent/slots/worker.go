package slots

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iamlovingit/clawmanager-agent/control"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type Worker struct {
	network       *networkOwner
	cfg           gateway.Config
	manager       *gateway.GatewayManager
	handler       http.Handler
	mu            sync.Mutex
	book          *book
	lease         *os.File
	writerPath    string
	writerRequest gateway.CreateGatewayRequest
	slotName      string
	metrics       *metricsSampler
}

func NewWorker(cfg gateway.Config) (*Worker, error) {
	if cfg.ControlToken == "" || cfg.Capacity != 1 {
		return nil, fmt.Errorf("worker requires control token and capacity one")
	}
	children := []string{"teams"}
	if cfg.RuntimeType != "" {
		children = append(children, cfg.RuntimeType)
	}
	if err := workspaceio.PrepareManagementRoots(cfg.WorkspaceRoot, children...); err != nil {
		return nil, fmt.Errorf("prepare trusted runtime workspace: %w", err)
	}
	cfg.IsolatedLifecycle = true
	cfg.GatewayBaseEnv = imageEnvironment(cfg.Runtime)
	b, err := openBook(cfg.AgentDataDir)
	if err != nil {
		return nil, err
	}
	network := &networkOwner{}
	manager := gateway.NewGatewayManager(cfg, sandboxStarter{cfg: cfg, network: network}, nil)
	if cfg.Runtime != nil && cfg.Runtime.HealthChecker(cfg) != nil {
		manager.SetHealthChecker(cfg.Runtime.HealthChecker(cfg))
	} else {
		manager.SetHealthChecker(gateway.NewHTTPGatewayHealthChecker(cfg))
	}
	slot := os.Getenv("RUNTIME_SLOT_INDEX")
	if slot == "" {
		slot = "0"
	}
	worker := &Worker{cfg: cfg, network: network, manager: manager, book: b, slotName: "slot-" + slot, metrics: newMetricsSampler()}
	worker.handler = control.NewControlHandler(cfg, manager, discardReporter{})
	return worker, nil
}

type discardReporter struct{}

func (discardReporter) ReportHeartbeat(context.Context, gateway.HeartbeatPayload) error { return nil }
func (discardReporter) ReportSkills(context.Context, gateway.SkillReportPayload) error  { return nil }
func (discardReporter) PodID() int                                                      { return 0 }

func (w *Worker) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(gateway.ControlTokenHeader)), []byte(w.cfg.ControlToken)) != 1 {
		http.Error(out, "unauthorized", 401)
		return
	}
	if r.URL.Path == "/v1/slot-skills" && r.Method == http.MethodGet {
		writeJSON(out, 200, gateway.BuildSkillReportPayload(w.cfg, w.manager, 0, "full"))
		return
	}
	if r.URL.Path == "/v1/slot-metrics" && r.Method == http.MethodGet {
		if w.metrics == nil {
			writeJSON(out, 200, unavailableContainerMetrics("metrics_sampler_unavailable"))
			return
		}
		writeJSON(out, 200, w.metrics.Sample())
		return
	}
	if r.URL.Path == "/v1/slot-state" && r.Method == http.MethodGet {
		w.mu.Lock()
		defer w.mu.Unlock()
		snapshot := Snapshot{Gateways: w.manager.GatewayStates(), Assignment: w.book.Assignment, Quarantined: w.book.quarantined || w.manager.Health() != nil || w.network.unavailable()}
		writeJSON(out, 200, snapshot)
		return
	}
	if r.URL.Path == "/v1/gateways/cancel-pending" && r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil {
			http.Error(out, "invalid cancellation", 400)
			return
		}
		req, err := decodePendingCancellation(data, w.cfg)
		if err != nil || req.GatewayPort != w.cfg.GatewayPortStart {
			http.Error(out, "invalid cancellation scope", 400)
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if err = w.cancelPending(req); err != nil {
			http.Error(out, "pending cancellation is not confirmed", 409)
			return
		}
		writeJSON(out, 200, map[string]any{"gateway_id": fmt.Sprintf("gw-%d-%d", req.InstanceID, req.Generation), "stopped": true, "confirmed": true, "unmounted": true})
		return
	}
	if r.URL.Path == "/v1/gateways" && r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			http.Error(out, "invalid request", 400)
			return
		}
		var req gateway.CreateGatewayRequest
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&req) != nil || req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.UID < 1 || req.GID < 1 || req.AgentType != w.cfg.RuntimeType || req.GatewayPort != w.cfg.GatewayPortStart || req.PodUID != w.cfg.PodUID {
			http.Error(out, "invalid slot identity or port", 400)
			return
		}
		if !matchingNetworkProfile(w.cfg.NetworkSecurityProfile, req.SecurityProfile) {
			http.Error(out, "invalid slot network profile", 400)
			return
		}
		if _, err = gateway.ValidateWorkspacePath(w.cfg.WorkspaceRoot, w.cfg.RuntimeType, req); err != nil {
			http.Error(out, "invalid instance workspace", 400)
			return
		}
		if w.cfg.NetworkSecurityProfile != "" {
			if _, err = validateNetworkAssignment(w.cfg, req, req.GatewayPort); err != nil {
				http.Error(out, "invalid private network assignment", 400)
				return
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.book.quarantined {
			http.Error(out, "slot requires writer reconciliation", 409)
			return
		}
		var lease *os.File
		if w.lease == nil {
			lease, err = w.acquire(req)
			if err != nil {
				http.Error(out, "workspace writer lease is active or unavailable", 409)
				return
			}
		}
		if err = w.book.begin(req); err != nil {
			if lease != nil {
				_ = stopWriter(w.writerPath, w.writerRequest, w.slotName)
				lease.Close()
			}
			http.Error(out, err.Error(), 409)
			return
		}
		if lease != nil {
			w.lease = lease
		}
		copyReq := r.Clone(r.Context())
		copyReq.Body = io.NopCloser(bytes.NewReader(data))
		// Preserve ownership even when the response fails: async startup may have
		// begun, and only explicit confirmed stop can release the reservation.
		w.handler.ServeHTTP(out, copyReq)
		return
	}
	stop := strings.HasPrefix(r.URL.Path, "/v1/gateways/") && (r.Method == http.MethodDelete || (r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop")))
	if stop {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/gateways/"), "/stop")
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.book.quarantined || w.book.Assignment == nil || w.book.Assignment.GatewayID != id {
			http.Error(out, "slot cannot confirm unknown writer stop", 409)
			return
		}
		if w.book.Assignment.State == "stopped" {
			writeJSON(out, 200, map[string]any{"gateway_id": id, "stopped": true, "confirmed": true, "unmounted": true})
			return
		}
		if err := w.manager.StopGatewayConfirmed(r.Context(), id); err != nil {
			http.Error(out, "gateway stop is not confirmed", 409)
			return
		}
		if err := stopWriter(w.writerPath, w.writerRequest, w.slotName); err != nil {
			http.Error(out, "failed to persist writer stop", 409)
			return
		}
		if err := w.book.stopped(id); err != nil {
			http.Error(out, "failed to persist stop confirmation", 409)
			return
		}
		if w.lease != nil {
			if err := w.lease.Close(); err != nil {
				w.book.quarantined = true
				http.Error(out, "failed to release writer lease", 409)
				return
			}
			w.lease = nil
		}
		writeJSON(out, 200, map[string]any{"gateway_id": id, "stopped": true, "confirmed": true, "unmounted": true})
		return
	}
	w.handler.ServeHTTP(out, r)
}

func (w *Worker) writerPaths(req gateway.CreateGatewayRequest) (string, string, error) {
	if err := ensureInstanceParents(w.cfg.WorkspaceRoot, w.cfg.RuntimeType, req.UserID); err != nil {
		return "", "", err
	}
	lockDir := filepath.Join(w.cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", "", err
	}
	if err := trustedDirectory(lockDir); err != nil {
		return "", "", err
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("%s-%d-%d.lock", w.cfg.RuntimeType, req.UserID, req.InstanceID))
	if err := trustedFile(lockPath); err != nil {
		return "", "", err
	}
	writerPath := filepath.Join(lockDir, fmt.Sprintf("%s-%d-%d.json", w.cfg.RuntimeType, req.UserID, req.InstanceID))
	if err := trustedFile(writerPath); err != nil {
		return "", "", err
	}
	return lockPath, writerPath, nil
}

func (w *Worker) acquire(req gateway.CreateGatewayRequest) (*os.File, error) {
	lockPath, writerPath, err := w.writerPaths(req)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockWorkspace(f); err != nil {
		f.Close()
		return nil, err
	}
	w.writerPath = writerPath
	if err = trustedFile(w.writerPath); err != nil {
		f.Close()
		return nil, err
	}
	if err = claimWriter(w.writerPath, req, w.slotName); err != nil {
		f.Close()
		return nil, err
	}
	w.writerRequest = req
	return f, nil
}

func (w *Worker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.book.Assignment == nil || w.book.Assignment.State == "stopped" {
		return w.manager.StopAll(ctx)
	}
	if w.book.quarantined {
		return fmt.Errorf("unreconciled writer state")
	}
	id := w.book.Assignment.GatewayID
	if err := w.manager.StopGatewayConfirmed(ctx, id); err != nil {
		return err
	}
	if err := stopWriter(w.writerPath, w.writerRequest, w.slotName); err != nil {
		return err
	}
	if err := w.book.stopped(id); err != nil {
		return err
	}
	if w.lease != nil {
		err := w.lease.Close()
		w.lease = nil
		return err
	}
	return nil
}

func matchingNetworkProfile(configured, requested string) bool {
	return (configured == "" && requested == "") || (configured == "isolated-network-v1" && requested == configured)
}
