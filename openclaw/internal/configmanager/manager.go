package configmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

type revisionClient interface {
	FetchConfigRevision(ctx context.Context, id string) (protocol.ConfigRevisionResponse, error)
}

type Manager struct {
	cfg    appconfig.Config
	client revisionClient
	store  *store.Store

	mu                 sync.Mutex
	modelGuardSnapshot modelGuardBaseline
	modelBaselineSet   bool
}

func New(cfg appconfig.Config, client revisionClient, st *store.Store) *Manager {
	return &Manager{cfg: cfg, client: client, store: st}
}

func (m *Manager) ApplyRevision(ctx context.Context, revisionID string) (map[string]any, error) {
	resp, err := m.client.FetchConfigRevision(ctx, revisionID)
	if err != nil {
		return nil, err
	}

	content, err := decodeContent(resp)
	if err != nil {
		return nil, err
	}

	if err := verifyChecksum(content, resp.Checksum); err != nil {
		return nil, err
	}
	if err := validateJSON(content); err != nil {
		return nil, err
	}
	content, _, err = normalizeConfigMap(content, m.cfg)
	if err != nil {
		return nil, err
	}
	if err := validateJSON(content); err != nil {
		return nil, err
	}
	baselineParsed, err := parseConfigJSON(content)
	if err != nil {
		return nil, err
	}

	stagingDir := filepath.Join(m.cfg.AgentDataDir, "config-staging")
	backupDir := filepath.Join(m.cfg.AgentDataDir, "config-backup")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	stagingFile := filepath.Join(stagingDir, "openclaw.json")
	if err := os.WriteFile(stagingFile, content, 0o600); err != nil {
		return nil, fmt.Errorf("write staging config: %w", err)
	}

	activeFile := m.cfg.OpenClawConfigPath
	backupFile := filepath.Join(backupDir, "openclaw.json")
	if err := os.MkdirAll(filepath.Dir(activeFile), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir active dir: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir backup: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rollbackSucceeded := true
	if data, err := os.ReadFile(activeFile); err == nil {
		rollbackSucceeded = os.WriteFile(backupFile, data, 0o600) == nil
	}
	if err := os.Rename(stagingFile, activeFile); err != nil {
		return nil, fmt.Errorf("activate config: %w", err)
	}
	if err := m.setModelBaselineLocked(baselineParsed); err != nil {
		return nil, err
	}

	if err := m.store.Update(func(s *store.State) {
		s.LastApplyAttemptRevisionID = revisionID
		s.LastApplyTime = time.Now().UTC()
		s.CurrentConfigRevisionID = revisionID
		s.LastGoodConfigRevisionID = revisionID
	}); err != nil {
		return nil, err
	}

	return map[string]any{
		"revision_id":        revisionID,
		"rollback_succeeded": rollbackSucceeded,
		"active_path":        activeFile,
	}, nil
}

func decodeContent(resp protocol.ConfigRevisionResponse) ([]byte, error) {
	return resp.Content, nil
}

func verifyChecksum(data []byte, checksum string) error {
	if checksum == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	checksum = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(checksum), "sha256:"))
	if !strings.EqualFold(actual, checksum) {
		return fmt.Errorf("checksum mismatch: expected %s got %s", checksum, actual)
	}
	return nil
}

func validateJSON(data []byte) error {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("validate config json: %w", err)
	}
	return nil
}
