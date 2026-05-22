package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

type registerClient interface {
	Register(ctx context.Context, req protocol.RegisterRequest) (protocol.RegisterResponse, error)
}

type Manager struct {
	cfg    appconfig.Config
	client registerClient
	store  *store.Store
}

func New(cfg appconfig.Config, client registerClient, st *store.Store) *Manager {
	return &Manager{cfg: cfg, client: client, store: st}
}

func (m *Manager) Ensure(ctx context.Context) (store.State, error) {
	state := m.store.Snapshot()
	if state.AgentID != "" && state.SessionToken != "" && time.Until(state.SessionExpiresAt) > 2*time.Minute {
		return state, nil
	}

	agentID := state.AgentID
	if agentID == "" {
		agentID = m.cfg.RuntimeType + "-" + m.cfg.InstanceID + "-main"
	}

	resp, err := m.client.Register(ctx, protocol.RegisterRequest{
		InstanceID:      mustAtoi(m.cfg.InstanceID),
		AgentID:         agentID,
		AgentVersion:    protocol.AgentVersion,
		ProtocolVersion: m.cfg.ProtocolVersion,
		Capabilities:    registerCapabilities(),
		HostInfo:        collectHostInfo(m.cfg),
	})
	if err != nil {
		return state, err
	}

	if agentID == "" || resp.SessionToken == "" {
		return state, errors.New("register response missing agent_id or session_token")
	}

	if err := m.store.Update(func(s *store.State) {
		s.AgentID = agentID
		s.SessionToken = resp.SessionToken
		s.SessionExpiresAt = resp.SessionExpiresAt
		s.LastRegisterTime = time.Now().UTC()
		if s.CurrentConfigRevisionID == "" {
			s.CurrentConfigRevisionID = m.cfg.InitialConfigRevisionID
		}
		if s.LastGoodConfigRevisionID == "" {
			s.LastGoodConfigRevisionID = m.cfg.InitialConfigRevisionID
		}
	}); err != nil {
		return state, fmt.Errorf("persist session: %w", err)
	}

	return m.store.Snapshot(), nil
}

func mustAtoi(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func collectHostInfo(cfg appconfig.Config) map[string]any {
	hostname, _ := os.Hostname()
	host := map[string]any{
		"runtime_type":   cfg.RuntimeType,
		"runtime_name":   cfg.RuntimeName,
		"runtime":        cfg.RuntimeType,
		"image":          cfg.RuntimeImage,
		"desktop_base":   cfg.DesktopBase,
		"persistent_dir": cfg.DiskUsagePath,
		"port":           cfg.RuntimePort,
		"hostname":       hostname,
		"goos":           runtime.GOOS,
		"goarch":         runtime.GOARCH,
		"arch":           runtime.GOARCH,
	}
	if cfg.RuntimeImage == "" {
		delete(host, "image")
	}
	if cfg.RuntimePort <= 0 {
		delete(host, "port")
	}
	return host
}

func registerCapabilities() []string {
	return []string{
		"runtime.status",
		"runtime.health",
		"metrics.report",
		"skills.inventory",
		"skills.upload",
		"commands.poll",
		"llm.gateway",
		"config.apply",
		"process.management",
		"local-debug-http",
	}
}

func ShouldReRegister(err error) bool {
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		code := statusErr.StatusCode()
		return code == http.StatusUnauthorized || code == http.StatusForbidden
	}
	return false
}
