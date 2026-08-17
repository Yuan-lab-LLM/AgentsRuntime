package gateway

import (
	"context"
	"errors"
	"time"
)

const (
	ControlTokenHeader = "X-ClawManager-Control-Token"
	AgentTokenHeader   = "X-ClawManager-Agent-Token"
)

var (
	ErrNoFreePort         = errors.New("no free port")
	ErrDraining           = errors.New("runtime pod is draining")
	ErrRuntimeType        = errors.New("agent_type does not match runtime type")
	ErrWorkspacePath      = errors.New("workspace path is outside requested instance")
	ErrStaleGeneration    = errors.New("stale gateway generation")
	ErrGatewayStartFailed = errors.New("gateway start failed")
)

type Config struct {
	RuntimeType           string
	Runtime               RuntimeProfile
	WorkspaceRoot         string
	GatewayPortStart      int
	GatewayPortEnd        int
	GatewayPortBlockSize  int
	GatewayAuthMode       string
	ControlToken          string
	ReportToken           string
	BackendURL            string
	ListenAddr            string
	PublicPort            int
	ImageRef              string
	Namespace             string
	PodName               string
	PodUID                string
	PodIP                 string
	NodeName              string
	DeploymentName        string
	AgentEndpoint         string
	Capacity              int
	GatewayCommand        []string
	GatewayToken          string
	PublicOrigin          string
	AllowedOrigins        []string
	TrustedProxies        []string
	LLMBaseURL            string
	LLMAPIKey             string
	LLMAPIKeySet          bool
	LLMModelIDs           []string
	LLMReasoning          map[string]bool
	LLMReasoningControl   map[string]string
	AgentDataDir          string
	HeartbeatInterval     time.Duration
	MetricsInterval       time.Duration
	GatewayReportInterval time.Duration
	SkillsReportInterval  time.Duration
	RegisterRetryInterval time.Duration
	ProcessStopTimeout    time.Duration
	GatewayStartupTimeout time.Duration
}

type RuntimeDefaults struct {
	WorkspaceRoot         string
	AgentDataDir          string
	GatewayPortStart      int
	GatewayPortEnd        int
	GatewayPortBlockSize  int
	GatewayCapacity       int
	GatewayAuthMode       string
	GatewayStartupTimeout time.Duration
}

type RuntimeProfile interface {
	Type() string
	DisplayName() string
	Defaults() RuntimeDefaults
	GatewayCommand(authMode string) []string
	GatewayEnv(base []string, cfg Config, req CreateGatewayRequest, workspacePath string, port int) []string
	PrepareWorkspace(cfg Config, req CreateGatewayRequest, workspacePath string) error
	WriteGatewayConfig(cfg Config, req CreateGatewayRequest, workspacePath string, port int) error
	HealthChecker(cfg Config) GatewayHealthChecker
}

type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type CreateGatewayRequest struct {
	InstanceID    int               `json:"instance_id"`
	UserID        int               `json:"user_id"`
	AgentType     string            `json:"agent_type"`
	WorkspacePath string            `json:"workspace_path"`
	GatewayPort   int               `json:"gateway_port,omitempty"`
	PortRange     PortRange         `json:"port_range"`
	UID           int               `json:"uid"`
	GID           int               `json:"gid"`
	CPUCores      int               `json:"cpu_cores"`
	MemoryMB      int               `json:"memory_mb"`
	DiskQuotaMB   int               `json:"disk_quota_mb"`
	Generation    int               `json:"generation"`
	RequestID     string            `json:"request_id,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

type CreateGatewayResponse struct {
	GatewayID     string `json:"gateway_id"`
	InstanceID    int    `json:"instance_id"`
	Port          int    `json:"port"`
	PID           *int   `json:"pid,omitempty"`
	Status        string `json:"status"`
	WorkspacePath string `json:"workspace_path"`
}

type GatewayStartSpec struct {
	GatewayID     string
	RuntimeType   string
	InstanceID    int
	UserID        int
	WorkspacePath string
	Port          int
	UID           int
	GID           int
	CPUCores      int
	MemoryMB      int
	DiskQuotaMB   int
	Generation    int
	Command       []string
	Env           []string
}

type ManagedProcess struct {
	PID  int
	Stop func(context.Context) error
	Done <-chan error
}

type ProcessStarter interface {
	StartGateway(context.Context, GatewayStartSpec) (ManagedProcess, error)
}

type GatewayHealthChecker interface {
	WaitReady(context.Context, GatewayStartSpec) error
}

type GatewayState struct {
	InstanceID    int       `json:"instance_id"`
	UserID        int       `json:"user_id,omitempty"`
	GatewayID     string    `json:"gateway_id"`
	RuntimeType   string    `json:"runtime_type"`
	WorkspacePath string    `json:"workspace_path"`
	Port          int       `json:"gateway_port"`
	PortAlias     int       `json:"port,omitempty"`
	PID           int       `json:"gateway_pid,omitempty"`
	UID           int       `json:"uid,omitempty"`
	GID           int       `json:"gid,omitempty"`
	CPUCores      int       `json:"cpu_cores,omitempty"`
	MemoryMB      int       `json:"memory_mb,omitempty"`
	DiskQuotaMB   int       `json:"disk_quota_mb,omitempty"`
	Generation    int       `json:"generation"`
	State         string    `json:"state"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	HealthAt      time.Time `json:"health_at,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type RegisterPayload struct {
	RuntimeType    string    `json:"runtime_type"`
	Namespace      string    `json:"namespace"`
	PodName        string    `json:"pod_name"`
	PodUID         string    `json:"pod_uid,omitempty"`
	PodIP          string    `json:"pod_ip"`
	NodeName       string    `json:"node_name,omitempty"`
	DeploymentName string    `json:"deployment_name"`
	ImageRef       string    `json:"image_ref"`
	AgentEndpoint  string    `json:"agent_endpoint"`
	State          string    `json:"state"`
	Capacity       int       `json:"capacity"`
	MaxGateways    int       `json:"max_gateways"`
	UsedSlots      int       `json:"used_slots"`
	AvailableSlots int       `json:"available_slots"`
	Draining       bool      `json:"draining"`
	ReportedAt     time.Time `json:"reported_at"`
}

type RegisterResponse struct {
	PodID int `json:"pod_id,omitempty"`
	Pod   struct {
		ID int `json:"id"`
	} `json:"pod,omitempty"`
}

type HeartbeatPayload struct {
	PodID          int       `json:"pod_id,omitempty"`
	Namespace      string    `json:"namespace"`
	PodName        string    `json:"pod_name"`
	State          string    `json:"state"`
	MaxGateways    int       `json:"max_gateways"`
	UsedSlots      int       `json:"used_slots"`
	AvailableSlots int       `json:"available_slots"`
	Draining       bool      `json:"draining"`
	ReportedAt     time.Time `json:"reported_at"`
}

type MetricsPayload struct {
	PodID           int            `json:"pod_id,omitempty"`
	Namespace       string         `json:"namespace"`
	PodName         string         `json:"pod_name"`
	CPUMillisUsed   int64          `json:"cpu_millis_used"`
	MemoryBytesUsed uint64         `json:"memory_bytes_used"`
	DiskBytesUsed   uint64         `json:"disk_bytes_used"`
	NetworkRXBytes  uint64         `json:"network_rx_bytes"`
	NetworkTXBytes  uint64         `json:"network_tx_bytes"`
	Metrics         map[string]any `json:"metrics"`
	ReportedAt      time.Time      `json:"reported_at"`
}

type GatewayReportPayload struct {
	PodID     int            `json:"pod_id,omitempty"`
	Namespace string         `json:"namespace"`
	PodName   string         `json:"pod_name"`
	Gateways  []GatewayState `json:"gateways"`
}

type SkillReportPayload struct {
	PodID       int                   `json:"pod_id,omitempty"`
	RuntimeType string                `json:"runtime_type"`
	Namespace   string                `json:"namespace"`
	PodName     string                `json:"pod_name"`
	ReportedAt  time.Time             `json:"reported_at"`
	Mode        string                `json:"mode"`
	Instances   []SkillInstanceReport `json:"instances"`
}

type SkillInstanceReport struct {
	InstanceID    int           `json:"instance_id"`
	WorkspacePath string        `json:"workspace_path"`
	Skills        []SkillRecord `json:"skills"`
}

type SkillRecord struct {
	SkillID      string `json:"skill_id"`
	SkillVersion string `json:"skill_version,omitempty"`
	Identifier   string `json:"identifier"`
	InstallPath  string `json:"install_path"`
	ContentMD5   string `json:"content_md5,omitempty"`
	Source       string `json:"source"`
	Type         string `json:"type"`
}

type HeartbeatReporter interface {
	ReportHeartbeat(context.Context, HeartbeatPayload) error
}

// SkillsReporter is implemented by the runtime agent process control plane uses
// when ClawManager requests an on-demand skill inventory resync.
type SkillsReporter interface {
	ReportSkills(context.Context, SkillReportPayload) error
	PodID() int
}
