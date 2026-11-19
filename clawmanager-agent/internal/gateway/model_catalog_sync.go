package gateway

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

// A receipt for the startup write, not a live availability/health assertion.
type ModelCatalogSyncReport struct {
	State       string    `json:"state"`
	OwnerUserID int       `json:"owner_user_id"`
	Generation  int       `json:"generation"`
	Revision    string    `json:"revision,omitempty"`
	ModelCount  int       `json:"model_count"`
	ObservedAt  time.Time `json:"observed_at"`
}

type startupCatalogWriter interface{ WritesStartupModelCatalog() bool }

// Native writers run in the child startup script, after WriteGatewayConfig.
// Readiness alone is not a receipt; the profile must observe a current write.
type nativeStartupCatalogWriter interface {
	StartupModelCatalogWritten(GatewayStartSpec, CreateGatewayRequest, time.Time) bool
}

func (m *GatewayManager) recordNativeModelCatalogSync(gatewayID string, req CreateGatewayRequest, spec GatewayStartSpec, startedAt time.Time) {
	writer, ok := m.profile().(nativeStartupCatalogWriter)
	if ok && writer.StartupModelCatalogWritten(spec, req, startedAt) {
		m.recordModelCatalogSyncWithProof(gatewayID, req, "applied", true)
	}
}

func (m *GatewayManager) recordModelCatalogSync(gatewayID string, req CreateGatewayRequest, phase string) {
	m.recordModelCatalogSyncWithProof(gatewayID, req, phase, false)
}

func (m *GatewayManager) recordModelCatalogSyncWithProof(gatewayID string, req CreateGatewayRequest, phase string, nativeWritten bool) {
	writer, ok := m.profile().(startupCatalogWriter)
	if (!ok || !writer.WritesStartupModelCatalog()) && !nativeWritten {
		return
	}
	flag, _ := RequestEnvValue(req, "CLAWMANAGER_MODEL_CATALOG_SYNC")
	if flag != "1" {
		return
	}
	if _, present := RequestEnvValue(req, "CLAWMANAGER_MODEL_CATALOG_JSON"); !present {
		return
	}
	report := &ModelCatalogSyncReport{State: phase, OwnerUserID: req.UserID, Generation: req.Generation, ObservedAt: time.Now().UTC()}
	if phase == "applied" {
		raw, present := RequestEnvValue(req, "CLAWMANAGER_MODEL_CATALOG_JSON")
		if !present || len(raw) > 4*1024*1024 {
			return
		} // No proof for an unrecorded live fetch.
		var snapshot struct {
			Complete    bool   `json:"complete"`
			Protocol    int    `json:"catalog_protocol"`
			Generation  int    `json:"generation"`
			OwnerUserID int    `json:"owner_user_id"`
			Revision    string `json:"revision"`
			Models      *[]struct {
				ID string `json:"id"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(raw), &snapshot) != nil || !snapshot.Complete || snapshot.Protocol != 2 || snapshot.Generation != req.Generation || snapshot.OwnerUserID != req.UserID || snapshot.Models == nil {
			return
		}
		if revision, err := hex.DecodeString(snapshot.Revision); err != nil || len(revision) != 32 {
			return
		}
		report.Revision = snapshot.Revision
		for _, model := range *snapshot.Models {
			if model.ID != "auto" {
				report.ModelCount++
			}
		}
		if len(*snapshot.Models) == 0 {
			report.State = "empty"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if record := m.gateways[gatewayID]; record != nil && record.state.InstanceID == req.InstanceID && record.state.UserID == req.UserID && record.state.Generation == req.Generation {
		record.state.ModelCatalogSync = report
		m.notifyGatewayStateChangedLocked()
	}
}
