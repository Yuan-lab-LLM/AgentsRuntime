package scheduledtasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ManagedJobIDPrefix = "cm-st-"
	EnvHermes          = "CLAWMANAGER_HERMES_SCHEDULED_TASKS_JSON"
	EnvRuntime         = "CLAWMANAGER_RUNTIME_SCHEDULED_TASKS_JSON"
	EnvOpenClaw        = "CLAWMANAGER_OPENCLAW_SCHEDULED_TASKS_JSON"
)

type PayloadEnvelope struct {
	SchemaVersion int          `json:"schemaVersion"`
	Items         []PayloadItem `json:"items"`
}

type PayloadItem struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Key     string          `json:"key"`
	Name    string          `json:"name"`
	Version int             `json:"version"`
	Tags    []string        `json:"tags"`
	Content json.RawMessage `json:"content"`
}

type ResourceContent struct {
	SchemaVersion int             `json:"schemaVersion"`
	Kind          string          `json:"kind"`
	Format        string          `json:"format"`
	Config        json.RawMessage `json:"config"`
}

type CronStoreFile struct {
	Version int       `json:"version"`
	Jobs    []CronJob `json:"jobs"`
}

type CronJob struct {
	ID            string          `json:"id"`
	AgentID       string          `json:"agentId,omitempty"`
	SessionKey    string          `json:"sessionKey,omitempty"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Enabled       bool            `json:"enabled"`
	DeleteAfterRun bool           `json:"deleteAfterRun,omitempty"`
	CreatedAtMs   int64           `json:"createdAtMs"`
	UpdatedAtMs   int64           `json:"updatedAtMs"`
	Schedule      json.RawMessage `json:"schedule"`
	SessionTarget string          `json:"sessionTarget"`
	WakeMode      string          `json:"wakeMode"`
	Payload       json.RawMessage `json:"payload"`
	Delivery      json.RawMessage `json:"delivery,omitempty"`
	State         map[string]any  `json:"state"`
}

type ApplyResult struct {
	SourceEnv     string   `json:"source_env"`
	RawSHA256     string   `json:"raw_sha256"`
	JobCount      int      `json:"job_count"`
	Skipped       bool     `json:"skipped"`
	StorePath     string   `json:"store_path"`
	Error         string   `json:"error,omitempty"`
	IgnoredFields []string `json:"ignored_fields,omitempty"`
}

type jobConfig struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Enabled        *bool           `json:"enabled,omitempty"`
	DeleteAfterRun *bool           `json:"deleteAfterRun,omitempty"`
	AgentID        string          `json:"agentId,omitempty"`
	SessionKey     string          `json:"sessionKey,omitempty"`
	Schedule       json.RawMessage `json:"schedule"`
	SessionTarget  string          `json:"sessionTarget"`
	WakeMode       string          `json:"wakeMode"`
	Payload        json.RawMessage `json:"payload"`
	Delivery       json.RawMessage `json:"delivery,omitempty"`
}

// ReadScheduledTasksEnv returns the first non-empty scheduled tasks payload and its env name.
func ReadScheduledTasksEnv(getenv func(string) string) (envName string, raw string) {
	for _, key := range []string{EnvHermes, EnvRuntime, EnvOpenClaw} {
		value := strings.TrimSpace(getenv(key))
		if value != "" {
			return key, value
		}
	}
	return "", ""
}

// ApplyOpenClawFromEnv merges ClawManager managed cron jobs into an OpenClaw cron store.
func ApplyOpenClawFromEnv(openclawHome string, getenv func(string) string) ApplyResult {
	return ApplyFromEnv(OpenClawCronStorePath(openclawHome), getenv)
}

// ApplyFromEnv merges ClawManager managed cron jobs into storePath (OpenClaw schema).
func ApplyFromEnv(storePath string, getenv func(string) string) ApplyResult {
	result := ApplyResult{StorePath: storePath}
	envName, raw := ReadScheduledTasksEnv(getenv)
	if envName == "" {
		result.Skipped = true
		return result
	}
	result.SourceEnv = envName
	sum := sha256.Sum256([]byte(raw))
	result.RawSHA256 = hex.EncodeToString(sum[:])

	jobs, err := jobsFromPayload(raw)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.JobCount = len(jobs)

	existing, err := loadStore(storePath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if openClawManagedSetMatches(existing.Jobs, jobs, result.RawSHA256) {
		result.Skipped = true
		return result
	}

	if err := upsertManagedJobs(storePath, existing, jobs, result.RawSHA256); err != nil {
		result.Error = err.Error()
		return result
	}
	return result
}

func openClawManagedSetMatches(existingJobs, managed []CronJob, payloadHash string) bool {
	existingManaged := map[string]string{}
	for _, job := range existingJobs {
		if !strings.HasPrefix(job.ID, ManagedJobIDPrefix) {
			continue
		}
		hash := ""
		if job.State != nil {
			if value, ok := job.State["clawmanagerPayloadSHA256"].(string); ok {
				hash = value
			}
		}
		existingManaged[job.ID] = hash
	}
	if len(existingManaged) != len(managed) {
		return false
	}
	for _, job := range managed {
		hash, ok := existingManaged[job.ID]
		if !ok || hash != payloadHash {
			return false
		}
	}
	return true
}

func jobsFromPayload(raw string) ([]CronJob, error) {
	var payload PayloadEnvelope
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("parse scheduled tasks payload: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	jobs := make([]CronJob, 0, len(payload.Items))
	for _, item := range payload.Items {
		if strings.TrimSpace(strings.ToLower(item.Type)) != "" &&
			strings.TrimSpace(strings.ToLower(item.Type)) != "scheduled_task" {
			continue
		}
		var content ResourceContent
		if err := json.Unmarshal(item.Content, &content); err != nil {
			return nil, fmt.Errorf("parse scheduled task content for id=%d: %w", item.ID, err)
		}
		var cfg jobConfig
		if err := json.Unmarshal(content.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parse scheduled task config for id=%d: %w", item.ID, err)
		}
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		if name == "" {
			name = item.Key
		}
		enabled := true
		if cfg.Enabled != nil {
			enabled = *cfg.Enabled
		}
		deleteAfterRun := false
		if cfg.DeleteAfterRun != nil {
			deleteAfterRun = *cfg.DeleteAfterRun
		}
		jobID := ManagedJobIDPrefix + strconv.Itoa(item.ID)
		jobs = append(jobs, CronJob{
			ID:             jobID,
			AgentID:        strings.TrimSpace(cfg.AgentID),
			SessionKey:     strings.TrimSpace(cfg.SessionKey),
			Name:           name,
			Description:    cfg.Description,
			Enabled:        enabled,
			DeleteAfterRun: deleteAfterRun,
			CreatedAtMs:    now,
			UpdatedAtMs:    now,
			Schedule:       cfg.Schedule,
			SessionTarget:  strings.TrimSpace(cfg.SessionTarget),
			WakeMode:       strings.TrimSpace(cfg.WakeMode),
			Payload:        cfg.Payload,
			Delivery:       cfg.Delivery,
			State:          map[string]any{},
		})
	}
	return jobs, nil
}

func upsertManagedJobs(storePath string, existing CronStoreFile, managed []CronJob, payloadHash string) error {
	if err := os.MkdirAll(filepath.Dir(storePath), 0o750); err != nil {
		return fmt.Errorf("create cron store dir: %w", err)
	}

	kept := make([]CronJob, 0, len(existing.Jobs))
	for _, job := range existing.Jobs {
		if strings.HasPrefix(job.ID, ManagedJobIDPrefix) {
			continue
		}
		kept = append(kept, job)
	}

	now := time.Now().UTC().UnixMilli()
	existingByID := map[string]CronJob{}
	for _, job := range existing.Jobs {
		existingByID[job.ID] = job
	}
	for _, job := range managed {
		if prev, ok := existingByID[job.ID]; ok {
			job.CreatedAtMs = prev.CreatedAtMs
			if prev.State != nil {
				job.State = prev.State
			}
		} else {
			job.CreatedAtMs = now
		}
		job.UpdatedAtMs = now
		if job.State == nil {
			job.State = map[string]any{}
		}
		job.State["clawmanagerPayloadSHA256"] = payloadHash
		kept = append(kept, job)
	}

	next := CronStoreFile{Version: 1, Jobs: kept}
	return saveStore(storePath, next)
}

func loadStore(path string) (CronStoreFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CronStoreFile{Version: 1, Jobs: []CronJob{}}, nil
		}
		return CronStoreFile{}, fmt.Errorf("read cron store: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return CronStoreFile{Version: 1, Jobs: []CronJob{}}, nil
	}
	var store CronStoreFile
	if err := json.Unmarshal(data, &store); err != nil {
		return CronStoreFile{}, fmt.Errorf("parse cron store: %w", err)
	}
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Jobs == nil {
		store.Jobs = []CronJob{}
	}
	return store, nil
}

func saveStore(path string, store CronStoreFile) error {
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cron store: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o640); err != nil {
		return fmt.Errorf("write cron store temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace cron store: %w", err)
	}
	return nil
}

// OpenClawCronStorePath returns the OpenClaw cron jobs.json under openclawHome.
func OpenClawCronStorePath(openclawHome string) string {
	return filepath.Join(openclawHome, "cron", "jobs.json")
}

// HermesCronStorePath returns the Hermes cron jobs.json under hermesHome.
func HermesCronStorePath(hermesHome string) string {
	return filepath.Join(hermesHome, "cron", "jobs.json")
}
