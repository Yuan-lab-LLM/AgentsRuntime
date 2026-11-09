package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func registerConfirmedStopRoutes(mux *http.ServeMux, cfg gateway.Config, manager *gateway.GatewayManager) {
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if cfg.ControlToken == "" {
			http.Error(w, "control token unavailable", http.StatusServiceUnavailable)
			return false
		}
		return authorizeControl(cfg, w, r)
	}
	mux.HandleFunc("/v1/stop-capabilities", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, manager.StopCapabilities())
	})
	mux.HandleFunc("/v1/gateway-stop", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		var req gateway.ConfirmStopRequest
		if err := decoder.Decode(&req); err != nil || !req.Valid() {
			http.Error(w, "invalid stop request", http.StatusBadRequest)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid trailing data", http.StatusBadRequest)
			return
		}
		result, err := manager.ConfirmStop(r.Context(), req)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, gateway.ErrGatewayStopConflict):
				status = http.StatusConflict
			case errors.Is(err, gateway.ErrGatewayStopUnsupported):
				status = http.StatusNotImplemented
			case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				status = http.StatusGatewayTimeout
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}
