package llmconfig

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartupCatalogReplacesStaleModelsWithStableIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llm/runtime/catalog" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect instance request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"complete": true, "catalog_protocol": 2, "revision": strings.Repeat("a", 64), "generation": 4, "models": []any{
			map[string]any{"id": "same name", "stable_id": "cm-deploy-v1-new", "context_window": 256000, "vision": true}, map[string]any{"id": "auto", "context_window": 128000},
		}})
	}))
	defer server.Close()
	old := Settings{BaseURL: server.URL + "/llm", APIKey: "test-key", ModelIDs: []string{"deleted"}, ModelNames: map[string]string{"deleted": "old"}}
	got, err := RefreshStartupCatalog(old, 4)
	if err != nil || len(got.ModelIDs) != 2 || got.ModelIDs[0] != "auto" || got.ModelIDs[1] != "cm-deploy-v1-new" || got.ModelNames[got.ModelIDs[1]] != "same name" || !got.CatalogSynced {
		t.Fatalf("bad catalog: %+v %v", got, err)
	}
	if old.ModelIDs[0] != "deleted" || old.ModelNames["deleted"] != "old" {
		t.Fatal("input mutated")
	}
	if got.ContextWindows[got.ModelIDs[1]] != 256000 || !got.Vision[got.ModelIDs[1]] {
		t.Fatal("capabilities lost")
	}
}

func TestCanonicalAuthoritativeEmptyUsesArray(t *testing.T) {
	raw, err := json.Marshal(Settings{CatalogSynced: true, ModelIDs: []string{}}.Canonical("clawmanager"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ids, ok := doc["model_ids"].([]any)
	if !ok || len(ids) != 0 {
		t.Fatal("empty catalog must not become null or a fallback model", string(raw))
	}
}

func TestStartupSnapshotWorksBeforeIsolatedProxyAndChecksOwner(t *testing.T) {
	snapshot := `{"complete":true,"catalog_protocol":2,"revision":"` + strings.Repeat("a", 64) + `","generation":7,"owner_user_id":1,"models":[{"id":"same name","stable_id":"cm-deploy-v1-new","context_window":256000}]}`
	req := gateway.CreateGatewayRequest{UserID: 1, Generation: 7, Environment: map[string]string{
		"CLAWMANAGER_MODEL_CATALOG_SYNC": "1", "CLAWMANAGER_MODEL_CATALOG_JSON": snapshot,
		"CLAWMANAGER_LLM_BASE_URL": "http://clawmanager-local-model.invalid:31281/api/v1/gateway/llm", "CLAWMANAGER_LLM_API_KEY": "cm-local-model",
	}}
	got, err := ResolveGateway(gateway.Config{}, req, ResolveOptions{})
	if err != nil || len(got.ModelIDs) != 1 || got.ModelIDs[0] != "cm-deploy-v1-new" {
		t.Fatal("startup depended on not-yet-started proxy", err)
	}
	req.UserID = 2
	if _, err = ResolveGateway(gateway.Config{}, req, ResolveOptions{}); err == nil {
		t.Fatal("cross-owner snapshot accepted")
	}
	req.UserID = 1
	req.Generation = 8
	if _, err = ResolveGateway(gateway.Config{}, req, ResolveOptions{}); err == nil {
		t.Fatal("stale generation accepted")
	}
}

func TestStartupCatalogFailuresAreNotAnEmptyCatalog(t *testing.T) {
	for _, body := range []string{`{}`, `{"complete":true,"catalog_protocol":2,"generation":4,"models":[]}`, `{"complete":true,"catalog_protocol":2,"revision":"` + strings.Repeat("a", 64) + `","generation":5,"models":[]}`, `{"complete":true,"catalog_protocol":2,"revision":"` + strings.Repeat("a", 64) + `","generation":4,"models":null}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if _, err := RefreshStartupCatalog(Settings{BaseURL: server.URL, APIKey: "secret"}, 4); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe failure", err)
			}
		})
	}
}

func TestStartupCatalogAuthoritativeEmptyAndNoRedirect(t *testing.T) {
	redirect := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"complete":true,"catalog_protocol":2,"revision":"` + strings.Repeat("a", 64) + `","generation":4,"models":[]}`))
	}))
	defer server.Close()
	got, err := RefreshStartupCatalog(Settings{BaseURL: server.URL, APIKey: "test"}, 4)
	if err != nil || !got.CatalogSynced || len(got.ModelIDs) != 0 {
		t.Fatal("empty is not authoritative", err)
	}
	redirect = true
	if _, err = RefreshStartupCatalog(Settings{BaseURL: server.URL, APIKey: "test"}, 4); err == nil {
		t.Fatal("followed redirect")
	}
}
