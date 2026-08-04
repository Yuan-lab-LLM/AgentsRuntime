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

type HermesJobsFile struct {
	Jobs      []HermesJob `json:"jobs"`
	UpdatedAt string      `json:"updated_at,omitempty"`
}

type HermesJob struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Prompt      string         `json:"prompt"`
	Schedule    map[string]any `json:"schedule"`
	Skills      []string       `json:"skills"`
	Skill       any            `json:"skill"`
	Deliver     string         `json:"deliver"`
	Repeat      map[string]any `json:"repeat"`
	State       string         `json:"state"`
	Enabled     bool           `json:"enabled"`
	NextRunAt   *string        `json:"next_run_at"`
	LastRunAt   any            `json:"last_run_at"`
	LastStatus  any            `json:"last_status"`
	CreatedAt   string         `json:"created_at"`
	Model       any            `json:"model"`
	Provider    any            `json:"provider"`
	Script      any            `json:"script"`
	ScheduleDisplay string     `json:"schedule_display,omitempty"`
}

// ApplyHermesFromEnv translates OpenClaw-benchmark scheduled tasks into Hermes
// native cron jobs and upserts managed entries into hermes jobs.json.
// uid/gid should be the instance Linux IDs so gateway processes can read the store.
func ApplyHermesFromEnv(hermesHome string, getenv func(string) string, uid, gid int) ApplyResult {
	storePath := HermesCronStorePath(hermesHome)
	result := ApplyResult{
		StorePath:     storePath,
		IgnoredFields: []string{"wakeMode", "sessionTarget"},
	}
	envName, raw := ReadScheduledTasksEnv(getenv)
	if envName == "" {
		result.Skipped = true
		return result
	}
	result.SourceEnv = envName
	sum := sha256Sum(raw)
	result.RawSHA256 = sum

	hashPath := filepath.Join(hermesHome, "cron", "clawmanager-scheduled-tasks.sha256")
	if previous, err := os.ReadFile(hashPath); err == nil && strings.TrimSpace(string(previous)) == sum {
		existing, loadErr := loadHermesJobs(storePath)
		if loadErr == nil {
			managedCount := 0
			for _, job := range existing.Jobs {
				if strings.HasPrefix(job.ID, ManagedJobIDPrefix) {
					managedCount++
				}
			}
			openClawJobs, parseErr := jobsFromPayload(raw)
			if parseErr == nil && managedCount == len(openClawJobs) {
				result.JobCount = len(openClawJobs)
				result.Skipped = true
				if err := ownHermesCronArtifacts(hermesHome, storePath, hashPath, uid, gid); err != nil {
					result.Error = err.Error()
				}
				return result
			}
		}
	}

	openClawJobs, err := jobsFromPayload(raw)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	hermesJobs := make([]HermesJob, 0, len(openClawJobs))
	for _, job := range openClawJobs {
		hj, err := translateOpenClawJobToHermes(job, hermesHome, uid, gid)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		hermesJobs = append(hermesJobs, hj)
	}
	result.JobCount = len(hermesJobs)
	if err := upsertHermesManagedJobs(storePath, hermesJobs, uid, gid); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.MkdirAll(filepath.Dir(hashPath), 0o750); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := ownPath(filepath.Dir(hashPath), uid, gid); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.WriteFile(hashPath, []byte(sum+"\n"), 0o640); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := ownHermesCronArtifacts(hermesHome, storePath, hashPath, uid, gid); err != nil {
		result.Error = err.Error()
		return result
	}
	return result
}

func translateOpenClawJobToHermes(job CronJob, hermesHome string, uid, gid int) (HermesJob, error) {
	prompt, model, err := extractHermesPrompt(job)
	if err != nil {
		return HermesJob{}, err
	}
	schedule, display, err := translateScheduleToHermes(job.Schedule)
	if err != nil {
		return HermesJob{}, err
	}
	deliver, webhookURL, err := translateDeliveryToHermes(job.Delivery)
	if err != nil {
		return HermesJob{}, err
	}
	if webhookURL != "" {
		prompt = appendWebhookInstruction(prompt, webhookURL)
		if err := writeHermesWebhookURL(hermesHome, job.ID, webhookURL, uid, gid); err != nil {
			return HermesJob{}, err
		}
	}

	now := time.Now().UTC()
	created := now.Format(time.RFC3339)
	next := computeHermesNextRunHint(schedule, now)
	hj := HermesJob{
		ID:              job.ID,
		Name:            job.Name,
		Prompt:          prompt,
		Schedule:        schedule,
		Skills:          []string{},
		Skill:           nil,
		Deliver:         deliver,
		Repeat:          map[string]any{"times": nil, "completed": 0},
		State:           "scheduled",
		Enabled:         job.Enabled,
		NextRunAt:       next,
		LastRunAt:       nil,
		LastStatus:      nil,
		CreatedAt:       created,
		Model:           model,
		Provider:        nil,
		Script:          nil,
		ScheduleDisplay: display,
	}
	if !job.Enabled {
		hj.State = "paused"
	}
	if job.DeleteAfterRun {
		hj.Repeat = map[string]any{"times": 1, "completed": 0}
	}
	return hj, nil
}

func extractHermesPrompt(job CronJob) (string, any, error) {
	var payload map[string]any
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return "", nil, fmt.Errorf("hermes translate payload: %w", err)
	}
	kind, _ := payload["kind"].(string)
	var model any
	if value, ok := payload["model"]; ok {
		model = value
	}
	switch kind {
	case "agentTurn":
		message, _ := payload["message"].(string)
		if strings.TrimSpace(message) == "" {
			return "", nil, fmt.Errorf("hermes translate: agentTurn message required")
		}
		return message, model, nil
	case "systemEvent":
		text, _ := payload["text"].(string)
		if strings.TrimSpace(text) == "" {
			return "", nil, fmt.Errorf("hermes translate: systemEvent text required")
		}
		// Hermes cron always runs an agent prompt; mirror systemEvent as explicit instruction.
		return "System event (from ClawManager scheduled task):\n" + text, model, nil
	default:
		return "", nil, fmt.Errorf("hermes translate: unsupported payload.kind %q", kind)
	}
}

func translateScheduleToHermes(raw json.RawMessage) (map[string]any, string, error) {
	var schedule map[string]any
	if err := json.Unmarshal(raw, &schedule); err != nil {
		return nil, "", fmt.Errorf("hermes translate schedule: %w", err)
	}
	kind, _ := schedule["kind"].(string)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "cron":
		expr, _ := schedule["expr"].(string)
		expr = strings.TrimSpace(expr)
		if expr == "" {
			return nil, "", fmt.Errorf("hermes translate: cron expr required")
		}
		return map[string]any{
			"kind":    "cron",
			"expr":    expr,
			"display": expr,
		}, expr, nil
	case "every":
		everyMs, err := asInt64(schedule["everyMs"])
		if err != nil || everyMs <= 0 {
			return nil, "", fmt.Errorf("hermes translate: everyMs must be > 0")
		}
		minutes := everyMs / 60000
		if minutes < 1 {
			minutes = 1
		}
		display := fmt.Sprintf("every %dm", minutes)
		return map[string]any{
			"kind":    "interval",
			"minutes": minutes,
			"display": display,
		}, display, nil
	case "at":
		at, _ := schedule["at"].(string)
		at = strings.TrimSpace(at)
		if at == "" {
			return nil, "", fmt.Errorf("hermes translate: at timestamp required")
		}
		display := "once at " + at
		return map[string]any{
			"kind":    "once",
			"run_at":  at,
			"display": display,
		}, display, nil
	default:
		return nil, "", fmt.Errorf("hermes translate: unsupported schedule.kind %q", kind)
	}
}

func translateDeliveryToHermes(raw json.RawMessage) (deliver string, webhookURL string, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "origin", "", nil
	}
	var delivery map[string]any
	if err := json.Unmarshal(raw, &delivery); err != nil {
		return "", "", fmt.Errorf("hermes translate delivery: %w", err)
	}
	mode, _ := delivery["mode"].(string)
	channel, _ := delivery["channel"].(string)
	to, _ := delivery["to"].(string)
	channel = strings.TrimSpace(channel)
	to = strings.TrimSpace(to)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "announce":
		if channel == "" || channel == "last" {
			return "origin", "", nil
		}
		if to != "" {
			return channel + ":" + to, "", nil
		}
		return channel, "", nil
	case "none":
		return "local", "", nil
	case "webhook":
		if to == "" {
			return "", "", fmt.Errorf("hermes translate: webhook delivery.to required")
		}
		return "local", to, nil
	default:
		return "", "", fmt.Errorf("hermes translate: unsupported delivery.mode %q", mode)
	}
}

func appendWebhookInstruction(prompt, webhookURL string) string {
	return prompt + "\n\n[ClawManager delivery.webhook]\n" +
		"After you finish, POST your complete final answer as JSON {\"text\": \"...\"} to " +
		webhookURL +
		" using an available HTTP tool. Treat webhook delivery as required."
}

func writeHermesWebhookURL(hermesHome, jobID, webhookURL string, uid, gid int) error {
	dir := filepath.Join(hermesHome, "cron", "webhooks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := ownPath(dir, uid, gid); err != nil {
		return err
	}
	path := filepath.Join(dir, jobID+".url")
	if err := os.WriteFile(path, []byte(webhookURL+"\n"), 0o640); err != nil {
		return err
	}
	return ownPath(path, uid, gid)
}

func computeHermesNextRunHint(schedule map[string]any, now time.Time) *string {
	kind, _ := schedule["kind"].(string)
	switch kind {
	case "once":
		if runAt, ok := schedule["run_at"].(string); ok && strings.TrimSpace(runAt) != "" {
			value := strings.TrimSpace(runAt)
			return &value
		}
	case "interval", "cron":
		value := now.UTC().Format(time.RFC3339)
		return &value
	}
	value := now.UTC().Format(time.RFC3339)
	return &value
}

func upsertHermesManagedJobs(storePath string, managed []HermesJob, uid, gid int) error {
	if err := os.MkdirAll(filepath.Dir(storePath), 0o750); err != nil {
		return fmt.Errorf("create hermes cron dir: %w", err)
	}
	if err := ownPath(filepath.Dir(storePath), uid, gid); err != nil {
		return err
	}
	existing, err := loadHermesJobs(storePath)
	if err != nil {
		return err
	}
	kept := make([]HermesJob, 0, len(existing.Jobs))
	existingByID := map[string]HermesJob{}
	for _, job := range existing.Jobs {
		existingByID[job.ID] = job
		if strings.HasPrefix(job.ID, ManagedJobIDPrefix) {
			continue
		}
		kept = append(kept, job)
	}
	for _, job := range managed {
		if prev, ok := existingByID[job.ID]; ok {
			job.CreatedAt = prev.CreatedAt
			job.LastRunAt = prev.LastRunAt
			job.LastStatus = prev.LastStatus
			if prev.Repeat != nil {
				if completed, ok := prev.Repeat["completed"]; ok {
					job.Repeat["completed"] = completed
				}
			}
		}
		kept = append(kept, job)
	}
	return saveHermesJobs(storePath, HermesJobsFile{
		Jobs:      kept,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, uid, gid)
}

func loadHermesJobs(path string) (HermesJobsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HermesJobsFile{Jobs: []HermesJob{}}, nil
		}
		return HermesJobsFile{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return HermesJobsFile{Jobs: []HermesJob{}}, nil
	}
	var asObject HermesJobsFile
	if err := json.Unmarshal(data, &asObject); err == nil {
		if asObject.Jobs == nil {
			asObject.Jobs = []HermesJob{}
		}
		return asObject, nil
	}
	var asList []HermesJob
	if err := json.Unmarshal(data, &asList); err != nil {
		return HermesJobsFile{}, fmt.Errorf("parse hermes cron store: %w", err)
	}
	return HermesJobsFile{Jobs: asList}, nil
}

func saveHermesJobs(path string, file HermesJobsFile, uid, gid int) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return ownPath(path, uid, gid)
}

func ownHermesCronArtifacts(hermesHome, storePath, hashPath string, uid, gid int) error {
	for _, path := range []string{
		filepath.Join(hermesHome, "cron"),
		storePath,
		hashPath,
		filepath.Join(hermesHome, "cron", "webhooks"),
	} {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := ownPath(path, uid, gid); err != nil {
			return err
		}
	}
	webhooksDir := filepath.Join(hermesHome, "cron", "webhooks")
	entries, err := os.ReadDir(webhooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := ownPath(filepath.Join(webhooksDir, entry.Name()), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func asInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	default:
		return 0, fmt.Errorf("not an int")
	}
}

func sha256Sum(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
