package slots

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// AcquireStoppedCommandWriter grants a trusted maintenance command the same
// single-writer fence as a gateway. Finish(true) requires namespace teardown
// proof; Finish(false), or a supervisor crash, leaves a durable quarantine.
func AcquireStoppedCommandWriter(cfg gateway.Config, req gateway.WorkspaceUpgradeRequest) (finish func(bool) error, err error) {
	if req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || cfg.PodUID == "" {
		return nil, fmt.Errorf("maintenance writer identity is incomplete")
	}
	if err = ensureInstanceParents(cfg.WorkspaceRoot, cfg.RuntimeType, req.UserID); err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	if err = trustedDirectory(dir); err != nil {
		return nil, err
	}
	base := filepath.Join(dir, fmt.Sprintf("%s-%d-%d", cfg.RuntimeType, req.UserID, req.InstanceID))
	if err = trustedFile(base + ".lock"); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(base+".lock", os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockWorkspace(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("instance has another writer: %w", err)
	}
	path := base + ".json"
	if err = trustedFile(path); err != nil {
		file.Close()
		return nil, err
	}
	previous, err := readWriter(path)
	if err != nil {
		file.Close()
		return nil, err
	}
	if previous.State != "stopped" || previous.InstanceID != req.InstanceID || previous.UserID != req.UserID || previous.RuntimeType != cfg.RuntimeType || previous.Generation != req.Generation {
		file.Close()
		return nil, fmt.Errorf("maintenance requires the exact confirmed-stopped writer generation")
	}
	active := *previous
	active.State, active.PodUID, active.Slot = "upgrading", cfg.PodUID, "maintenance"
	if err = atomicJSON(path, active); err != nil {
		file.Close()
		return nil, err
	}
	var once sync.Once
	var result error
	return func(confirmed bool) error {
		once.Do(func() {
			if confirmed {
				result = atomicJSON(path, previous)
			}
			closeErr := file.Close()
			if result == nil {
				result = closeErr
			}
		})
		return result
	}, nil
}
