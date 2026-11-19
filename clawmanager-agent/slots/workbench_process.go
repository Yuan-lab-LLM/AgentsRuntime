package slots

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"path/filepath"
	"sync"
)

// Tokens are per-attempt capabilities pinned to namespace + UID + start time.
// Snapshots are bounded; a user refresh is the only enumeration trigger.
func attachWorkbenchProcesses(process *gateway.ManagedProcess, tracker *sandbox.Tracker, spec gateway.GatewayStartSpec) {
	var mu sync.Mutex
	identities := map[string]sandbox.ProcessIdentity{}
	records := map[string]workbench.Process{}
	process.WorkbenchProcesses = func(ctx context.Context) ([]workbench.Process, error) {
		mu.Lock()
		defer mu.Unlock()
		owned, err := tracker.WorkbenchProcesses(ctx, uint32(spec.UID))
		if err != nil {
			return nil, workbench.Failure("ownership_unavailable", "Business process ownership could not be verified.")
		}
		next := map[string]sandbox.ProcessIdentity{}
		nextRecords := map[string]workbench.Process{}
		result := []workbench.Process{}
		for _, p := range owned {
			id := ""
			for token, old := range identities {
				if old == p.Identity {
					id = token
					break
				}
			}
			if id == "" {
				var random [16]byte
				if _, err = rand.Read(random[:]); err != nil {
					return nil, err
				}
				id = hex.EncodeToString(random[:])
			}
			cwd := p.Cwd
			if inside(spec.WorkspacePath, cwd) {
				cwd, _ = filepath.Rel(spec.WorkspacePath, cwd)
			}
			row := workbench.Process{ID: id, Name: p.Name, Command: p.Name, Cwd: cwd, State: "running", Source: "agent", StartedAt: p.StartedAt, CanStop: true, CanRestart: false}
			next[id] = p.Identity
			nextRecords[id] = row
			result = append(result, row)
		}
		identities, records = next, nextRecords
		return result, nil
	}
	process.WorkbenchStopProcess = func(ctx context.Context, id string) (workbench.Process, error) {
		mu.Lock()
		defer mu.Unlock()
		identity, ok := identities[id]
		if !ok {
			return workbench.Process{}, workbench.Failure("process_not_found", "Process is unavailable; refresh the process list.")
		}
		if err := tracker.StopWorkbenchProcess(ctx, uint32(spec.UID), identity); err != nil {
			return workbench.Process{}, workbench.Failure("ownership_changed", "Process ownership changed; refresh the process list.")
		}
		row := records[id]
		row.State = "stopping"
		row.CanStop = false
		records[id] = row
		return row, nil
	}
}
