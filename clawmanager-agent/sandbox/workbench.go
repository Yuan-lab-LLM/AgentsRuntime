package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type WorkbenchProcess struct {
	Identity  ProcessIdentity
	Name, Cwd string
	StartedAt time.Time
}

// WorkbenchProcesses is an explicit snapshot of the pinned gateway namespace.
// No command line or environment is exposed: either may contain credentials.
func (t *Tracker) WorkbenchProcesses(ctx context.Context, uid uint32) ([]WorkbenchProcess, error) {
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	if t.namespaceFile == nil || uid == 0 {
		return nil, fmt.Errorf("process namespace ownership is unavailable")
	}
	alive, err := t.initAlive()
	if err != nil || !alive {
		return nil, fmt.Errorf("gateway identity is unavailable")
	}
	primary, err := t.primaryGateway()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(t.procRoot)
	if err != nil {
		return nil, err
	}
	result := []WorkbenchProcess{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		pid, e := strconv.Atoi(entry.Name())
		if e != nil || pid <= 0 || pid == primary.PID || pid == t.identity.PID {
			continue
		}
		identity, e := t.readIdentity(pid)
		if e != nil {
			continue
		}
		if !t.ownsWorkbenchProcess(uid, identity) {
			continue
		}
		dir := filepath.Join(t.procRoot, entry.Name())
		name, e := readWorkbenchProc(filepath.Join(dir, "comm"), 256)
		if e != nil {
			continue
		}
		cwd, _ := os.Readlink(filepath.Join(dir, "cwd"))
		if !t.ownsWorkbenchProcess(uid, identity) {
			continue
		}
		result = append(result, WorkbenchProcess{Identity: identity, Name: strings.TrimSpace(string(name)), Cwd: cwd, StartedAt: processStartTime(t.procRoot, identity.StartTime)})
		if len(result) >= 256 {
			break
		}
	}
	return result, nil
}
func readWorkbenchProc(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
func (t *Tracker) ownsWorkbenchProcess(uid uint32, identity ProcessIdentity) bool {
	if t.namespaceFile == nil || uid == 0 {
		return false
	}
	rawStat, err := readWorkbenchProc(filepath.Join(t.procRoot, strconv.Itoa(identity.PID), "stat"), 4096)
	current, state, _, parseErr := parseProcessIdentity(identity.PID, rawStat)
	if err != nil || parseErr != nil || state == "Z" || state == "X" || current != identity {
		return false
	}
	namespace := "pid"
	if t.auxiliaryMountScope {
		namespace = "mnt"
	} else if t.containerLifecycle {
		namespace = "user"
	}
	dir := filepath.Join(t.procRoot, strconv.Itoa(identity.PID))
	info, err := os.Stat(filepath.Join(dir, "ns", namespace))
	if err != nil || !os.SameFile(t.namespaceInfo, info) {
		return false
	}
	raw, err := readWorkbenchProc(filepath.Join(dir, "status"), 64<<10)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 {
				return false
			}
			expected := strconv.FormatUint(uint64(uid), 10)
			for _, field := range fields[1:] {
				if field != expected {
					return false
				}
			}
			return true
		}
	}
	return false
}
func (t *Tracker) StopWorkbenchProcess(ctx context.Context, uid uint32, identity ProcessIdentity) error {
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	alive, err := t.initAlive()
	if err != nil || !alive || !t.ownsWorkbenchProcess(uid, identity) {
		return fmt.Errorf("process ownership changed")
	}
	primary, err := t.primaryGateway()
	if err != nil || identity.PID == primary.PID || identity.PID == t.identity.PID {
		return fmt.Errorf("gateway process cannot be stopped through workbench")
	}
	process, err := os.FindProcess(identity.PID)
	if err != nil {
		return err
	}
	defer process.Release()
	if err = process.WithHandle(func(uintptr) {}); err != nil {
		return fmt.Errorf("pinned process handle unavailable")
	}
	if !t.ownsWorkbenchProcess(uid, identity) {
		return fmt.Errorf("process identity changed while pinning")
	}
	return SignalProcess(process, uid, syscall.SIGTERM)
}
func processStartTime(root string, ticks uint64) time.Time {
	raw, err := readWorkbenchProc(filepath.Join(root, "uptime"), 128)
	if err != nil {
		return time.Time{}
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return time.Time{}
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(uptime * float64(time.Second))).Add(time.Duration(ticks) * 10 * time.Millisecond).UTC()
}

// PinWorkbenchNamespace is called after a fixed business helper's readiness
// rendezvous, never on bubblewrap's earlier info-fd identity alone.
func (t *Tracker) PinWorkbenchNamespace() error {
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	if t.namespaceFile != nil {
		return nil
	}
	alive, err := t.initAlive()
	if err != nil || !alive {
		return fmt.Errorf("business helper identity unavailable")
	}
	namespace := "pid"
	if t.auxiliaryMountScope {
		namespace = "mnt"
	} else if t.containerLifecycle {
		namespace = "user"
	}
	file, err := os.Open(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "ns", namespace))
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	t.namespaceFile, t.namespaceInfo = file, info
	return nil
}

// StopWorkbenchScope signals and drains observed tasks in a pinned native
// auxiliary user namespace. Namespace scans are not container termination proof:
// source-slot reuse always requires the existing native reset ledger.
func (t *Tracker) StopWorkbenchScope(ctx context.Context, uid uint32) error {
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	if t.namespaceFile == nil {
		return fmt.Errorf("auxiliary namespace was not pinned")
	}
	deadline := time.Now().Add(time.Second)
	emptyObserved := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(t.procRoot)
		if err != nil {
			return err
		}
		count := 0
		for _, entry := range entries {
			pid, e := strconv.Atoi(entry.Name())
			if e != nil || pid <= 0 {
				continue
			}
			// Reject unreadable namespace metadata for this business UID: a
			// permission failure must never masquerade as an empty scope.
			rawStatus, statusErr := readWorkbenchProc(filepath.Join(t.procRoot, entry.Name(), "status"), 64<<10)
			if statusErr != nil {
				if namespaceInitDisappeared(statusErr) {
					continue
				}
				return statusErr
			}
			business := false
			for _, line := range strings.Split(string(rawStatus), "\n") {
				if strings.HasPrefix(line, "Uid:") {
					fields := strings.Fields(line)
					business = len(fields) == 5 && fields[1] == strconv.FormatUint(uint64(uid), 10)
				}
			}
			if !business {
				continue
			}
			namespace := "pid"
			if t.auxiliaryMountScope {
				namespace = "mnt"
			} else if t.containerLifecycle {
				namespace = "user"
			}
			ns, nsErr := os.Stat(filepath.Join(t.procRoot, entry.Name(), "ns", namespace))
			if nsErr != nil {
				if namespaceInitDisappeared(nsErr) {
					continue
				}
				return nsErr
			}
			if !os.SameFile(t.namespaceInfo, ns) {
				continue
			}
			// Once a task positively belongs to this scope, uncertainty cannot
			// turn it into an empty scan. Even a PID race requires another pass.
			count++
			identity, e := t.readIdentity(pid)
			if e != nil {
				if namespaceInitDisappeared(e) {
					continue
				}
				return fmt.Errorf("verify owned terminal task: %w", e)
			}
			if !t.ownsWorkbenchProcess(uid, identity) {
				continue
			}
			process, e := os.FindProcess(pid)
			if e != nil {
				return e
			}
			e = process.WithHandle(func(uintptr) {})
			if e == nil && t.ownsWorkbenchProcess(uid, identity) {
				signal := syscall.SIGTERM
				if time.Now().After(deadline) {
					signal = syscall.SIGKILL
				}
				e = SignalProcess(process, uid, signal)
			}
			process.Release()
			if e != nil && !os.IsNotExist(e) && e != os.ErrProcessDone {
				return e
			}
		}
		if count == 0 && emptyObserved {
			return nil
		}
		emptyObserved = count == 0
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
