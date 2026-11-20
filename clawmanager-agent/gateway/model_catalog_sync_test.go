package gateway

import (
	"strings"
	"testing"
	"time"
)

type catalogReceiptProfile struct{ RuntimeProfile }

func (catalogReceiptProfile) WritesStartupModelCatalog() bool { return true }

func TestStartupCatalogReceiptDoesNotConfusePendingWriteOrOldGeneration(t *testing.T) {
	m := &GatewayManager{cfg: Config{Runtime: catalogReceiptProfile{}}, gateways: map[string]*gatewayRecord{"gw": {state: GatewayState{InstanceID: 7, UserID: 2, Generation: 3}}}}
	req := CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 3, Environment: map[string]string{"CLAWMANAGER_MODEL_CATALOG_SYNC": "1", "CLAWMANAGER_MODEL_CATALOG_JSON": `{"complete":true,"catalog_protocol":2,"owner_user_id":2,"generation":3,"revision":"` + strings.Repeat("a", 64) + `","models":[{"id":"auto"},{"id":"new-name"}]}`}}
	m.recordModelCatalogSync("gw", req, "syncing")
	if m.gateways["gw"].state.ModelCatalogSync.State != "syncing" {
		t.Fatal("request was labelled as applied")
	}
	m.recordModelCatalogSync("gw", req, "applied")
	if got := m.gateways["gw"].state.ModelCatalogSync; got.State != "applied" || got.ModelCount != 1 {
		t.Fatal("write receipt missing", got)
	}
	req.Generation = 2
	m.recordModelCatalogSync("gw", req, "failed")
	if m.gateways["gw"].state.ModelCatalogSync.State != "applied" {
		t.Fatal("old attempt overwrote current receipt")
	}
}

type nativeReceiptProfile struct {
	RuntimeProfile
	written bool
}

func (p nativeReceiptProfile) StartupModelCatalogWritten(GatewayStartSpec, CreateGatewayRequest, time.Time) bool {
	return p.written
}

func TestNativeCatalogWaitsForVerifiedChildWrite(t *testing.T) {
	m := &GatewayManager{cfg: Config{Runtime: nativeReceiptProfile{}}, gateways: map[string]*gatewayRecord{"gw": {state: GatewayState{InstanceID: 7, UserID: 2, Generation: 3}}}}
	req := CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 3, Environment: map[string]string{"CLAWMANAGER_MODEL_CATALOG_SYNC": "1", "CLAWMANAGER_MODEL_CATALOG_JSON": `{"complete":true,"catalog_protocol":2,"owner_user_id":2,"generation":3,"revision":"` + strings.Repeat("a", 64) + `","models":[]}`}}
	m.recordModelCatalogSync("gw", req, "applied")
	m.recordNativeModelCatalogSync("gw", req, GatewayStartSpec{}, time.Now())
	if m.gateways["gw"].state.ModelCatalogSync != nil {
		t.Fatal("generic no-op writer or readiness claimed success")
	}
	m.cfg.Runtime = nativeReceiptProfile{written: true}
	m.recordNativeModelCatalogSync("gw", req, GatewayStartSpec{}, time.Now())
	if got := m.gateways["gw"].state.ModelCatalogSync; got == nil || got.State != "empty" {
		t.Fatal("verified empty native write missing", got)
	}
}

func TestGenericGatewayEnvUsesLaunchGeneration(t *testing.T) {
	env := GenericGatewayEnv([]string{"CLAWMANAGER_GATEWAY_GENERATION=1"}, Config{}, CreateGatewayRequest{Generation: 8, Environment: map[string]string{"CLAWMANAGER_GATEWAY_GENERATION": "2"}}, "/workspace", 20000)
	if got := envValue(env, "CLAWMANAGER_GATEWAY_GENERATION"); got != "8" {
		t.Fatal("stale child catalog generation", got)
	}
}

func TestStartupCatalogReceiptRequiresAnObservedWriterAndSnapshot(t *testing.T) {
	m := &GatewayManager{cfg: Config{Runtime: catalogReceiptProfile{}}, gateways: map[string]*gatewayRecord{"gw": {state: GatewayState{InstanceID: 7, UserID: 2, Generation: 3}}}}
	req := CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 3, Environment: map[string]string{"CLAWMANAGER_MODEL_CATALOG_SYNC": "1"}}
	m.recordModelCatalogSync("gw", req, "applied")
	if m.gateways["gw"].state.ModelCatalogSync != nil {
		t.Fatal("assumed a live fetch was applied without a receipt")
	}
	req.Environment["CLAWMANAGER_MODEL_CATALOG_JSON"] = `{"invalid":true}`
	m.recordModelCatalogSync("gw", req, "applied")
	if m.gateways["gw"].state.ModelCatalogSync != nil {
		t.Fatal("invalid snapshot labelled as applied")
	}
}
