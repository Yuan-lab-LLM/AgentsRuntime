package openclaw

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/scheduledtasks"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type openClawIO struct{ *workspaceio.FS }

func openOpenClawIO(base, trustedRoot string) (*openClawIO, error) {
	base = filepath.Clean(base)
	if !filepath.IsAbs(base) {
		return nil, fmt.Errorf("OpenClaw workspace must be absolute")
	}
	anchor := filepath.Clean(trustedRoot)
	if strings.TrimSpace(trustedRoot) == "" {
		// Standalone adapter callers may not have prepared the instance yet.
		// Find an existing anchor without creating or following a symlink, then
		// create each descendant through pinned directory descriptors.
		anchor = base
		for {
			_, err := os.Lstat(anchor)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
			parent := filepath.Dir(anchor)
			if parent == anchor {
				return nil, err
			}
			anchor = parent
		}
	}
	root, err := workspaceio.CreateAt(anchor, base, 0o750)
	if err != nil {
		return nil, err
	}
	return &openClawIO{FS: root}, nil
}

func (s *openClawIO) mkdirTemp(parent, pattern string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	name := filepath.Join(parent, strings.TrimSuffix(pattern, "*")+hex.EncodeToString(nonce[:]))
	if err := s.Mkdir(name, 0o750); err != nil {
		return "", err
	}
	return name, nil
}

// These package-local entrypoints also serve existing standalone adapter users.
// Production WriteGatewayConfig shares its already-open instance root instead.
func seedOpenClawPluginRuntimeFrom(defaults string, req gateway.CreateGatewayRequest, active string) error {
	base := req.WorkspacePath
	if base == "" {
		base = active
	}
	s, err := openOpenClawIO(base, "")
	if err != nil {
		return err
	}
	defer s.Close()
	return s.seedOpenClawPluginRuntimeFrom(defaults, req, active)
}

func seedOpenClawRedisTeamPlugin(req gateway.CreateGatewayRequest, workspace string) error {
	s, err := openOpenClawIO(workspace, "")
	if err != nil {
		return err
	}
	defer s.Close()
	return s.seedOpenClawRedisTeamPlugin(req, workspace)
}

func ensureDingTalkOpenClawPeer(active string) error {
	s, err := openOpenClawIO(active, "")
	if err != nil {
		return err
	}
	defer s.Close()
	return s.ensureDingTalkOpenClawPeer(active)
}

func (s *openClawIO) applyScheduledTasks(home string, getenv func(string) string, uid, gid int) scheduledtasks.ApplyResult {
	store := scheduledtasks.OpenClawCronStorePath(home)
	result := scheduledtasks.ApplyResult{StorePath: store}
	if name, _ := scheduledtasks.ReadScheduledTasksEnv(getenv); name == "" {
		result.Skipped = true
		return result
	}
	existing, err := s.ReadFile(store)
	if err != nil && !os.IsNotExist(err) {
		result.Error = err.Error()
		return result
	}
	// The legacy merger uses a predictable .tmp name. Never expose that writer
	// to instance-controlled paths; only publish its finished bytes atomically.
	staging, err := os.MkdirTemp("", "cm-openclaw-cron-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(staging)
	staged := filepath.Join(staging, "jobs.json")
	if existing != nil {
		if err := os.WriteFile(staged, existing, 0o600); err != nil {
			result.Error = err.Error()
			return result
		}
	}
	result = scheduledtasks.ApplyFromEnv(staged, getenv, os.Getuid(), os.Getgid())
	result.StorePath = store
	if result.Error != "" {
		return result
	}
	data, err := os.ReadFile(staged)
	if os.IsNotExist(err) {
		return result
	}
	if err == nil {
		err = s.MkdirAll(filepath.Dir(store), 0o750)
	}
	if err == nil {
		err = s.WriteFileAtomic(store, data, 0o640)
	}
	if err == nil {
		err = s.Chown(filepath.Dir(store), uid, gid)
	}
	if err == nil {
		err = s.Chown(store, uid, gid)
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}
