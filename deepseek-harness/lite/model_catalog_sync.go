package deepseek

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// This file is written by prepare-managed-config only after its native config
// dump and atomic preset write succeed. It contains no credentials or URLs.
func (Profile) StartupModelCatalogWritten(spec gateway.GatewayStartSpec, req gateway.CreateGatewayRequest, startedAt time.Time) bool {
	env := map[string]string{}
	for _, item := range spec.Env {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	home := env["DSH_HOME"]
	if home == "" {
		if env["HOME"] == "" {
			return false
		}
		home = filepath.Join(env["HOME"], ".dsh")
	}
	file, err := os.Open(filepath.Join(home, ".clawmanager", "catalog-sync.json"))
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.ModTime().Before(startedAt) || info.ModTime().After(time.Now().Add(time.Minute)) {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return false
	}
	var receipt struct {
		InstanceID int    `json:"instance_id"`
		OwnerID    int    `json:"owner_user_id"`
		Generation int    `json:"generation"`
		Revision   string `json:"revision"`
		Count      int    `json:"model_count"`
	}
	var snapshot struct {
		Revision string `json:"revision"`
		Models   []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	snapshotRaw, present := gateway.RequestEnvValue(req, "CLAWMANAGER_MODEL_CATALOG_JSON")
	if !present || len(snapshotRaw) > 4*1024*1024 || json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil || json.Unmarshal(raw, &receipt) != nil {
		return false
	}
	count := 0
	for _, model := range snapshot.Models {
		if model.ID != "auto" {
			count++
		}
	}
	return receipt.InstanceID == req.InstanceID && receipt.OwnerID == req.UserID && receipt.Generation == req.Generation && receipt.Revision == snapshot.Revision && receipt.Count == count
}
