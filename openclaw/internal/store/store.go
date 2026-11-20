package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type State struct {
	OwnerUserID                int                           `json:"owner_user_id,omitempty"`
	AgentID                    string                        `json:"agent_id"`
	SessionToken               string                        `json:"session_token"`
	SessionExpiresAt           time.Time                     `json:"session_expires_at"`
	LastRegisterTime           time.Time                     `json:"last_register_time"`
	CurrentConfigRevisionID    string                        `json:"current_config_revision_id"`
	LastGoodConfigRevisionID   string                        `json:"last_good_config_revision_id"`
	LastApplyAttemptRevisionID string                        `json:"last_apply_attempt_revision_id"`
	LastApplyTime              time.Time                     `json:"last_apply_time"`
	LastCommandExecutionCache  map[string]any                `json:"last_command_execution_cache"`
	PendingEventQueue          []any                         `json:"pending_event_queue"`
	ManagedSkills              map[string]ManagedSkillRecord `json:"managed_skills"`
	LastSkillInventoryDigest   string                        `json:"last_skill_inventory_digest"`
	LastSkillFullSyncAt        time.Time                     `json:"last_skill_full_sync_at"`
	LastSkillIncrementalSyncAt time.Time                     `json:"last_skill_incremental_sync_at"`
}

type ManagedSkillRecord struct {
	SkillID      string    `json:"skill_id,omitempty"`
	SkillVersion string    `json:"skill_version,omitempty"`
	InstallPath  string    `json:"install_path"`
	ContentMD5   string    `json:"content_md5,omitempty"`
	Source       string    `json:"source,omitempty"`
	Status       string    `json:"status,omitempty"`
	InstalledAt  time.Time `json:"installed_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type Store struct {
	path  string
	state State
	mu    sync.RWMutex
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir store dir: %w", err)
	}
	s := &Store{
		path: filepath.Join(dir, "state.json"),
		state: State{
			LastCommandExecutionCache: map[string]any{},
			PendingEventQueue:         []any{},
			ManagedSkills:             map[string]ManagedSkillRecord{},
		},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.persistLocked()
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	if s.state.LastCommandExecutionCache == nil {
		s.state.LastCommandExecutionCache = map[string]any{}
	}
	if s.state.PendingEventQueue == nil {
		s.state.PendingEventQueue = []any{}
	}
	if s.state.ManagedSkills == nil {
		s.state.ManagedSkills = map[string]ManagedSkillRecord{}
	}
	return nil
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.state
	return out
}

func (s *Store) Update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
