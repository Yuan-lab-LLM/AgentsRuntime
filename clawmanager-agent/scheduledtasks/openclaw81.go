package scheduledtasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const DeclarationKeyPrefix = "clawmanager:scheduled-task:"

type CommandRunner interface {
	Run(context.Context, []string, []string) ([]byte, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, args, env []string) ([]byte, error) {
	command := exec.CommandContext(ctx, "openclaw", args...)
	// Replace matching ambient variables instead of appending duplicate names.
	// Managed Lite gateways use per-instance credentials while the Runtime
	// container may still carry a bootstrap token.
	command.Env = mergeCommandEnv(os.Environ(), env)
	return command.CombinedOutput()
}

type GatewayAuth struct {
	Mode     string
	Token    string
	Password string
}

type ReconcileResult struct {
	PayloadSHA256 string `json:"payload_sha256"`
	Desired       int    `json:"desired"`
	Applied       int    `json:"applied"`
	Removed       int    `json:"removed"`
}

func ReconcileOpenClaw81(ctx context.Context, raw string, port int, auth GatewayAuth, runner CommandRunner) (ReconcileResult, error) {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	if port <= 0 {
		return ReconcileResult{}, errors.New("gateway port is required")
	}
	jobs, err := JobsFromPayload(raw)
	if err != nil {
		return ReconcileResult{}, err
	}
	digest := sha256.Sum256([]byte(raw))
	result := ReconcileResult{PayloadSHA256: hex.EncodeToString(digest[:]), Desired: len(jobs)}
	env := []string{"OPENCLAW_GATEWAY_TOKEN=", "OPENCLAW_GATEWAY_PASSWORD="}
	switch strings.ToLower(strings.TrimSpace(auth.Mode)) {
	case "trusted-proxy":
		password := strings.TrimSpace(auth.Password)
		if password == "" {
			return result, errors.New("gateway password is required for trusted-proxy automation reconciliation")
		}
		env[1] = "OPENCLAW_GATEWAY_PASSWORD=" + password
	case "token", "":
		token := strings.TrimSpace(auth.Token)
		if token == "" {
			return result, errors.New("gateway token is required for token automation reconciliation")
		}
		env[0] = "OPENCLAW_GATEWAY_TOKEN=" + token
	default:
		return result, fmt.Errorf("unsupported gateway auth mode %q", auth.Mode)
	}
	desired := map[string]bool{}
	for _, job := range jobs {
		key := DeclarationKeyPrefix + strings.TrimPrefix(job.ID, ManagedJobIDPrefix)
		desired[key] = true
		args, err := automationAddArgs(job, key, port)
		if err != nil {
			return result, fmt.Errorf("scheduled task %s: %w", job.ID, err)
		}
		output, err := runner.Run(ctx, args, env)
		if err != nil {
			return result, boundedCommandError("automation add", output, err)
		}
		result.Applied++
	}
	listOutput, err := runner.Run(ctx, []string{"automations", "list", "--all", "--json", "--port", strconv.Itoa(port)}, env)
	if err != nil {
		return result, boundedCommandError("automation list", listOutput, err)
	}
	for _, existing := range managedAutomationRecords(listOutput) {
		if desired[existing.DeclarationKey] {
			continue
		}
		output, removeErr := runner.Run(ctx, []string{"automations", "remove", existing.ID, "--json", "--port", strconv.Itoa(port)}, env)
		if removeErr != nil {
			return result, boundedCommandError("automation remove", output, removeErr)
		}
		result.Removed++
	}
	return result, nil
}

func mergeCommandEnv(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	put := func(item string) {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}
	for _, item := range base {
		put(item)
	}
	for _, item := range overrides {
		put(item)
	}
	merged := make([]string, 0, len(order))
	for _, key := range order {
		merged = append(merged, values[key])
	}
	return merged
}

func automationAddArgs(job CronJob, declarationKey string, port int) ([]string, error) {
	args := []string{"automations", "add", "--declaration-key", declarationKey, "--name", job.Name, "--port", strconv.Itoa(port), "--json"}
	var schedule map[string]any
	if err := json.Unmarshal(job.Schedule, &schedule); err != nil {
		return nil, fmt.Errorf("parse schedule: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(schedule["kind"]))) {
	case "cron":
		expr := strings.TrimSpace(fmt.Sprint(schedule["expr"]))
		if expr == "" {
			return nil, errors.New("cron expr is required")
		}
		args = append(args, "--cron", expr)
		if tz := strings.TrimSpace(fmt.Sprint(schedule["tz"])); tz != "" && tz != "<nil>" {
			args = append(args, "--tz", tz)
		}
	case "every":
		ms, err := numberToInt64(schedule["everyMs"])
		if err != nil || ms <= 0 {
			return nil, errors.New("positive everyMs is required")
		}
		args = append(args, "--every", (time.Duration(ms) * time.Millisecond).String())
	case "at":
		at := strings.TrimSpace(fmt.Sprint(schedule["at"]))
		if at == "" || at == "<nil>" {
			at = strings.TrimSpace(fmt.Sprint(schedule["atMs"]))
		}
		if at == "" || at == "<nil>" {
			return nil, errors.New("at value is required")
		}
		args = append(args, "--at", at)
	default:
		return nil, errors.New("unsupported schedule kind")
	}
	var payload map[string]any
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["kind"]))) {
	case "agentturn", "agent_turn":
		message := strings.TrimSpace(fmt.Sprint(payload["message"]))
		if message == "" || message == "<nil>" {
			return nil, errors.New("agent message is required")
		}
		args = append(args, "--message", message)
	case "systemevent", "system_event":
		message := strings.TrimSpace(fmt.Sprint(payload["text"]))
		if message == "" || message == "<nil>" {
			message = strings.TrimSpace(fmt.Sprint(payload["message"]))
		}
		if message == "" || message == "<nil>" {
			return nil, errors.New("system event text is required")
		}
		args = append(args, "--system-event", message)
	default:
		return nil, errors.New("unsupported payload kind")
	}
	if job.AgentID != "" {
		args = append(args, "--agent", job.AgentID)
	}
	if job.SessionKey != "" {
		args = append(args, "--session-key", job.SessionKey)
	}
	if job.SessionTarget != "" {
		args = append(args, "--session", job.SessionTarget)
	}
	if job.WakeMode != "" {
		args = append(args, "--wake", job.WakeMode)
	}
	if job.Description != "" {
		args = append(args, "--description", job.Description)
	}
	if !job.Enabled {
		args = append(args, "--disabled")
	}
	if job.DeleteAfterRun {
		args = append(args, "--delete-after-run")
	}
	return args, nil
}

func numberToInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	default:
		return 0, errors.New("not a number")
	}
}

type automationRecord struct{ ID, DeclarationKey string }

func managedAutomationRecords(raw []byte) []automationRecord {
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	var rows []any
	switch typed := decoded.(type) {
	case []any:
		rows = typed
	case map[string]any:
		for _, key := range []string{"jobs", "automations", "items"} {
			if value, ok := typed[key].([]any); ok {
				rows = value
				break
			}
		}
	}
	result := []automationRecord{}
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok {
			continue
		}
		key := strings.TrimSpace(firstString(item, "declarationKey", "declaration_key"))
		id := strings.TrimSpace(firstString(item, "id", "jobId", "job_id"))
		if id != "" && strings.HasPrefix(key, DeclarationKeyPrefix) {
			result = append(result, automationRecord{ID: id, DeclarationKey: key})
		}
	}
	return result
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return fmt.Sprint(value)
		}
	}
	return ""
}

func boundedCommandError(action string, output []byte, err error) error {
	if len(output) > 2048 {
		output = output[:2048]
	}
	return fmt.Errorf("%s failed: %w: %s", action, err, strings.TrimSpace(string(output)))
}
