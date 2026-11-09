package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const ControlTokenHeader = gateway.ControlTokenHeader

const openClawUpgradeOperationTimeout = 15 * time.Minute

func writeUpgradeOperationError(w http.ResponseWriter, err error) {
	if errors.Is(err, gateway.ErrWriterLeaseActive) {
		// Keep the old controller's retry marker while newer controllers use the
		// typed HTTP status. Never turn an in-flight writer into rollback authority.
		http.Error(w, "runtime reconciliation is required before migration: "+err.Error(), http.StatusLocked)
		return
	}
	http.Error(w, err.Error(), http.StatusConflict)
}

func NewControlHandler(cfg gateway.Config, manager *gateway.GatewayManager, reporter gateway.HeartbeatReporter) http.Handler {
	mux := http.NewServeMux()
	registerConfirmedStopRoutes(mux, cfg, manager)
	if cfg.ProxyPolicyFile != "" {
		policy := gateway.NewProxyPolicyStore(cfg.ProxyPolicyFile, cfg.Namespace)
		mux.HandleFunc("/v1/proxy-policy", func(w http.ResponseWriter, r *http.Request) {
			if !authorizeControl(cfg, w, r) {
				return
			}
			if r.Method == http.MethodGet {
				writeJSON(w, http.StatusOK, policy.Snapshot())
				return
			}
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var req gateway.ProxyPolicyUpdate
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				http.Error(w, "invalid proxy policy", http.StatusBadRequest)
				return
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				http.Error(w, "invalid proxy policy", http.StatusBadRequest)
				return
			}
			result, err := policy.Update(req)
			if errors.Is(err, gateway.ErrProxyPolicyConflict) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if err != nil {
				http.Error(w, "proxy policy update failed", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, result)
		})
	}

	// Kubernetes probes must not carry the control token. This endpoint only
	// exposes traffic readiness; all management operations remain authenticated.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := manager.Health(); err != nil || !manager.ReadyForTraffic() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := manager.Health(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		status := "ready"
		if manager.UpgradeStandby() {
			status = "standby"
		} else if manager.Draining() {
			status = "draining"
		}
		writeJSON(w, http.StatusOK, gateway.HealthResponse{Status: status, Capabilities: manager.HealthCapabilities()})
	})

	mux.HandleFunc("/v1/gateways", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.CreateGatewayRequest
		body := r.Body
		if manager.IsolatedGatewayLifecycle() {
			body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		decoder := json.NewDecoder(body)
		if manager.IsolatedGatewayLifecycle() {
			decoder.DisallowUnknownFields()
		}
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if manager.IsolatedGatewayLifecycle() {
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
		}
		resp, err := manager.CreateGateway(r.Context(), req)
		if err != nil {
			writeCreateGatewayError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/v1/gateways/", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		escapedID := strings.TrimPrefix(r.URL.Path, "/v1/gateways/")
		stopRequested := strings.HasSuffix(escapedID, "/stop")
		if stopRequested {
			escapedID = strings.TrimSuffix(escapedID, "/stop")
		}
		gatewayID, err := url.PathUnescape(escapedID)
		if err != nil {
			http.Error(w, "invalid gateway id", http.StatusBadRequest)
			return
		}
		switch {
		case r.Method == http.MethodGet && !stopRequested:
			state, ok := manager.GatewayState(gatewayID)
			if !ok {
				http.Error(w, "gateway not found", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, state)
		case r.Method == http.MethodDelete || (r.Method == http.MethodPost && stopRequested):
			if err := manager.StopGatewayConfirmed(r.Context(), gatewayID); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"gateway_id": gatewayID, "stopped": true, "confirmed": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/openclaw/preflight", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := manager.PreflightWorkspace(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/session-sqlite/migrate", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		operationCtx, cancel := context.WithTimeout(r.Context(), openClawUpgradeOperationTimeout)
		defer cancel()
		result, err := manager.MigrateSessionSQLite(operationCtx, req)
		if err != nil {
			writeUpgradeOperationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/session-sqlite/migrate/status", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := manager.SessionSQLiteMigrationStatus(req)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "session migration receipt not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/upgrade/preflight", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		operationCtx, cancel := context.WithTimeout(r.Context(), openClawUpgradeOperationTimeout)
		defer cancel()
		result, err := manager.PreflightUpgradeCompatibility(operationCtx, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/session-sqlite/restore", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		operationCtx, cancel := context.WithTimeout(r.Context(), openClawUpgradeOperationTimeout)
		defer cancel()
		result, err := manager.RestoreSessionSQLite(operationCtx, req)
		if err != nil {
			writeUpgradeOperationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/session-sqlite/restore/status", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req gateway.WorkspaceUpgradeRequest
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := manager.SessionSQLiteRestoreStatus(req)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "session restore receipt not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("/v1/openclaw/upgrade/activate", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			RolloutID string `json:"rollout_id"`
		}
		if err := decodeRequiredJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := manager.ActivateUpgrade(req.RolloutID); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if reporter != nil {
			_ = reporter.ReportHeartbeat(context.Background(), manager.HeartbeatPayload(0))
		}
		writeJSON(w, http.StatusOK, map[string]any{"activated": true, "rollout_id": req.RolloutID})
	})

	mux.HandleFunc("/v1/openclaw/writer-leases/", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/v1/openclaw/writer-leases/")
		var body struct {
			gateway.WorkspaceUpgradeRequest
			Token      string `json:"token"`
			TTLSeconds int    `json:"ttl_seconds"`
		}
		if err := decodeRequiredJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.LeaseToken == "" {
			body.LeaseToken = body.Token
		}
		ttl := time.Duration(body.TTLSeconds) * time.Second
		switch action {
		case "acquire":
			lease, err := manager.AcquireWriterLease(body.WorkspaceUpgradeRequest, body.LeaseToken, ttl)
			if err != nil {
				writeUpgradeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, lease)
		case "renew":
			lease, err := manager.RenewWriterLease(body.WorkspaceUpgradeRequest, ttl)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, lease)
		case "release":
			if err := manager.ReleaseWriterLease(body.WorkspaceUpgradeRequest); err != nil {
				writeUpgradeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"released": true})
		default:
			http.Error(w, "unknown writer lease action", http.StatusNotFound)
		}
	})

	mux.HandleFunc("/v1/drain", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Draining bool `json:"draining"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength != 0 {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		manager.SetDraining(req.Draining)
		if reporter != nil {
			_ = reporter.ReportHeartbeat(context.Background(), manager.HeartbeatPayload(0))
		}
		writeJSON(w, http.StatusOK, map[string]bool{"draining": manager.Draining()})
	})

	mux.HandleFunc("/v1/skills/resync", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeControl(cfg, w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			InstanceID int    `json:"instance_id"`
			Mode       string `json:"mode"`
			Trigger    string `json:"trigger"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength != 0 {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		skillsReporter, ok := reporter.(gateway.SkillsReporter)
		if !ok || skillsReporter == nil {
			http.Error(w, "skills reporter unavailable", http.StatusServiceUnavailable)
			return
		}
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			mode = "full"
		}
		payload := gateway.BuildSkillReportPayloadForInstance(cfg, manager, skillsReporter.PodID(), mode, req.InstanceID)
		if err := skillsReporter.ReportSkills(r.Context(), payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":          true,
			"mode":        mode,
			"trigger":     strings.TrimSpace(req.Trigger),
			"instance_id": req.InstanceID,
			"instances":   len(payload.Instances),
		})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(noRedirectResponseWriter{ResponseWriter: w}, r)
	})
}

type noRedirectResponseWriter struct {
	http.ResponseWriter
}

func (w noRedirectResponseWriter) WriteHeader(code int) {
	if code >= 300 && code < 400 {
		code = http.StatusNotFound
	}
	w.ResponseWriter.WriteHeader(code)
}

func authorizeControl(cfg gateway.Config, w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(ControlTokenHeader) != cfg.ControlToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeCreateGatewayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gateway.ErrRuntimeType), errors.Is(err, gateway.ErrWorkspacePath):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, gateway.ErrGatewayStopConflict), errors.Is(err, gateway.ErrDraining), errors.Is(err, gateway.ErrUpgradeStandby), errors.Is(err, gateway.ErrNoFreePort), errors.Is(err, gateway.ErrStaleGeneration), errors.Is(err, gateway.ErrActiveGeneration), errors.Is(err, gateway.ErrWriterLeaseActive):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, gateway.ErrGatewayStartFailed):
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeRequiredJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}
