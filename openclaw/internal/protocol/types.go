package protocol

import (
	"encoding/json"
	"time"
)

var AgentVersion = "openclaw-agent-dev"

type RegisterRequest struct {
	InstanceID      int            `json:"instance_id"`
	AgentID         string         `json:"agent_id"`
	AgentVersion    string         `json:"agent_version"`
	ProtocolVersion string         `json:"protocol_version"`
	Capabilities    []string       `json:"capabilities,omitempty"`
	HostInfo        map[string]any `json:"host_info,omitempty"`
}

type AgentMetadata struct {
	AgentID         string   `json:"agent_id"`
	AgentVersion    string   `json:"agent_version"`
	ProtocolVersion string   `json:"protocol_version"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

type RegisterResponse struct {
	SessionToken               string    `json:"session_token"`
	SessionExpiresAt           time.Time `json:"session_expires_at"`
	HeartbeatIntervalSeconds   int       `json:"heartbeat_interval_seconds"`
	CommandPollIntervalSeconds int       `json:"command_poll_interval_seconds"`
	ServerTime                 time.Time `json:"server_time"`
}

type HeartbeatRequest struct {
	AgentID                 string         `json:"agent_id"`
	Timestamp               time.Time      `json:"timestamp"`
	OpenClawStatus          string         `json:"openclaw_status"`
	CurrentConfigRevisionID *int           `json:"current_config_revision_id,omitempty"`
	Summary                 map[string]any `json:"summary,omitempty"`
}

type HeartbeatResponse struct {
	ServerTime              time.Time `json:"server_time"`
	HasPendingCommand       bool      `json:"has_pending_command"`
	DesiredPowerState       string    `json:"desired_power_state"`
	DesiredConfigRevisionID *int      `json:"desired_config_revision_id,omitempty"`
}

type RuntimePayload struct {
	OpenClawStatus          string `json:"openclaw_status"`
	OpenClawPID             *int   `json:"openclaw_pid,omitempty"`
	OpenClawVersion         string `json:"openclaw_version,omitempty"`
	CurrentConfigRevisionID *int   `json:"current_config_revision_id,omitempty"`
}

type StateReportRequest struct {
	AgentID    string         `json:"agent_id"`
	ReportedAt time.Time      `json:"reported_at"`
	Agent      AgentMetadata  `json:"agent,omitempty"`
	Runtime    RuntimePayload `json:"runtime"`
	SystemInfo map[string]any `json:"system_info,omitempty"`
	Health     map[string]any `json:"health,omitempty"`
}

type Command struct {
	ID             int            `json:"id"`
	Type           string         `json:"command_type"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	Payload        map[string]any `json:"payload"`
}

type CommandEnvelope struct {
	Command *Command `json:"command"`
}

type CommandStartRequest struct {
	AgentID   string    `json:"agent_id"`
	StartedAt time.Time `json:"started_at"`
}

type CommandFinishRequest struct {
	AgentID      string         `json:"agent_id"`
	FinishedAt   time.Time      `json:"finished_at"`
	Status       string         `json:"status"`
	Result       map[string]any `json:"result,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type ConfigRevisionResponse struct {
	ID       int             `json:"id"`
	Checksum string          `json:"checksum"`
	Content  json.RawMessage `json:"content"`
	Status   string          `json:"status"`
}
