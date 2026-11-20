package llmconfig

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RefreshStartupCatalog runs before writers touch persistent configuration.
// Only a complete, generation-matched response can replace the bootstrap list.
// Failures deliberately return no replacement, never an empty guessed catalog.
func RefreshStartupCatalog(settings Settings, generation int) (Settings, error) {
	fail := func() (Settings, error) {
		return Settings{}, fmt.Errorf("MODEL_CATALOG_SYNC_FAILED: authorized model directory unavailable; existing configuration was not replaced")
	}
	u, err := url.Parse(settings.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(settings.APIKey) == "" {
		return fail()
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(settings.BaseURL, "/")+"/runtime/catalog", nil)
	if err != nil {
		return fail()
	}
	req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fail()
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return fail()
	}
	return decodeStartupCatalog(settings, generation, raw)
}

func decodeStartupCatalog(settings Settings, generation int, raw []byte) (Settings, error) {
	fail := func() (Settings, error) {
		return Settings{}, fmt.Errorf("MODEL_CATALOG_SYNC_FAILED: authorized model directory unavailable; existing configuration was not replaced")
	}
	if len(raw) > 4*1024*1024 {
		return fail()
	}
	var doc struct {
		OwnerID    int    `json:"owner_user_id"`
		Complete   bool   `json:"complete"`
		Protocol   int    `json:"catalog_protocol"`
		Revision   string `json:"revision"`
		Generation *int   `json:"generation"`
		Models     *[]struct {
			ID               string `json:"id"`
			StableID         string `json:"stable_id"`
			ContextWindow    int    `json:"context_window"`
			Tools            bool   `json:"tools"`
			Vision           bool   `json:"vision"`
			Reasoning        bool   `json:"reasoning"`
			ReasoningControl string `json:"reasoning_control"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &doc) != nil || !doc.Complete || doc.Protocol != 2 || doc.Generation == nil || *doc.Generation != generation || doc.Models == nil {
		return fail()
	}
	if revision, err := hex.DecodeString(doc.Revision); err != nil || len(revision) != 32 {
		return fail()
	}
	settings.ModelIDs = []string{}
	settings.CatalogOwnerID = doc.OwnerID
	settings.ModelNames = map[string]string{}
	settings.ContextWindows = map[string]int{}
	settings.Reasoning = map[string]bool{}
	settings.ReasoningControl = map[string]string{}
	settings.Vision = map[string]bool{}
	settings.ContextWindow = 0
	seen := map[string]bool{}
	for _, entry := range *doc.Models {
		id := entry.StableID
		if entry.ID == "auto" {
			id = "auto"
		} else if !strings.HasPrefix(id, "cm-deploy-v1-") {
			return fail()
		}
		if seen[id] || strings.TrimSpace(entry.ID) == "" || entry.ContextWindow < 0 || int64(entry.ContextWindow) > 2147483647 {
			return fail()
		}
		seen[id] = true
		settings.ModelIDs = append(settings.ModelIDs, id)
		settings.ModelNames[id] = entry.ID
		settings.ContextWindows[id] = entry.ContextWindow
		settings.Reasoning[id] = entry.Reasoning
		settings.ReasoningControl[id] = entry.ReasoningControl
		settings.Vision[id] = entry.Vision
		if id == "auto" {
			settings.ContextWindow = entry.ContextWindow
		}
	}
	// Prefer Auto for new conversations only; never rewrite a persisted session.
	for i, id := range settings.ModelIDs {
		if id == "auto" {
			settings.ModelIDs[0], settings.ModelIDs[i] = settings.ModelIDs[i], settings.ModelIDs[0]
			break
		}
	}
	settings.ModelsQualified = false
	settings.ModelSource = "runtime-catalog-v2"
	settings.CatalogSynced = true
	return settings, nil
}
