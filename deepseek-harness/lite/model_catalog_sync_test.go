package deepseek

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestNativeCatalogReceiptRequiresCurrentWriteAndLaunchIdentity(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".clawmanager")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "catalog-sync.json")
	revision := strings.Repeat("a", 64)
	req := gateway.CreateGatewayRequest{InstanceID: 42, UserID: 2, Generation: 8, Environment: map[string]string{"CLAWMANAGER_MODEL_CATALOG_JSON": `{"revision":"` + revision + `","models":[{"id":"auto"},{"id":"new"}]}`}}
	spec := gateway.GatewayStartSpec{Env: []string{"DSH_HOME=" + home}}
	p := NewProfile("deepseek-harness")
	started := time.Now().Add(-time.Second)
	if p.StartupModelCatalogWritten(spec, req, started) {
		t.Fatal("readiness without receipt accepted")
	}
	raw := `{"instance_id":42,"owner_user_id":2,"generation":8,"revision":"` + revision + `","model_count":1}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if !p.StartupModelCatalogWritten(spec, req, started) {
		t.Fatal("current native write not accepted")
	}
	req.Generation = 9
	if p.StartupModelCatalogWritten(spec, req, started) {
		t.Fatal("old generation accepted")
	}
	req.Generation = 8
	req.UserID = 3
	if p.StartupModelCatalogWritten(spec, req, started) {
		t.Fatal("other owner accepted")
	}
	req.UserID = 2
	old := started.Add(-time.Minute)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	if p.StartupModelCatalogWritten(spec, req, started) {
		t.Fatal("previous attempt receipt accepted")
	}
}
