//go:build linux

package gateway

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type isolatedProcessIdentity struct {
	Version    int    `json:"version"`
	InstanceID int    `json:"instance_id"`
	Generation int    `json:"generation"`
	PID        int    `json:"pid"`
	UID        uint32 `json:"uid"`
	BootID     string `json:"boot_id"`
	StartTicks uint64 `json:"start_ticks"`
}

type isolatedProcStat struct {
	State      string
	Group      int
	StartTicks uint64
}

func isolatedRecordName(instanceID, generation int) string {
	return strconv.Itoa(instanceID) + "-" + strconv.Itoa(generation) + ".json"
}

// The store belongs to the control agent, never an instance UID. Do not use
// the writable workspace as the source of truth for process ownership.
func openIsolatedProcessStore(cfg Config) (*os.Root, error) {
	if cfg.AgentDataDir == "" || !filepath.IsAbs(cfg.AgentDataDir) {
		return nil, fmt.Errorf("workspace_unavailable: an absolute agent data directory is required")
	}
	if cfg.WorkspaceRoot != "" {
		rel, err := filepath.Rel(cfg.WorkspaceRoot, cfg.AgentDataDir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("workspace_unavailable: process metadata must be outside instance workspaces")
		}
	}
	if err := os.MkdirAll(cfg.AgentDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace_unavailable: create agent process store")
	}
	info, err := os.Lstat(cfg.AgentDataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace_unavailable: agent data directory must not be a symlink")
	}
	root, err := os.OpenRoot(cfg.AgentDataDir)
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: open agent process store")
	}
	defer root.Close()
	if err := secureIsolatedStoreDirectory(root, info); err != nil {
		return nil, err
	}
	if err := root.Mkdir("processes", 0o700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("workspace_unavailable: create process metadata directory")
	}
	info, err = root.Lstat("processes")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace_unavailable: process metadata directory must not be a symlink")
	}
	store, err := root.OpenRoot("processes")
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: open process metadata directory")
	}
	if err := secureIsolatedStoreDirectory(store, info); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func secureIsolatedStoreDirectory(root *os.Root, expected os.FileInfo) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("workspace_unavailable: open process store directory")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !os.SameFile(expected, info) {
		return fmt.Errorf("workspace_unavailable: process store directory changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("workspace_unavailable: process store is not owned by the agent")
	}
	if err := directory.Chmod(0o700); err != nil {
		return fmt.Errorf("workspace_unavailable: secure process store directory")
	}
	return nil
}

func currentIsolatedBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("workspace_unavailable: cannot read kernel boot identity")
	}
	return strings.TrimSpace(string(data)), nil
}

func readIsolatedProcStat(pid int) (isolatedProcStat, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return isolatedProcStat{}, err
	}
	return parseIsolatedProcStat(data)
}

func parseIsolatedProcStat(data []byte) (isolatedProcStat, error) {
	// comm can contain spaces and parentheses; the final ')' closes field 2.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return isolatedProcStat{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return isolatedProcStat{}, fmt.Errorf("incomplete process stat")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return isolatedProcStat{}, fmt.Errorf("invalid process group")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return isolatedProcStat{}, fmt.Errorf("invalid process start identity")
	}
	return isolatedProcStat{State: fields[0], Group: group, StartTicks: start}, nil
}

func recordIsolatedProcess(cfg Config, spec GatewayStartSpec, pid int) (func(), error) {
	if pid <= 1 || spec.InstanceID <= 0 || spec.Generation <= 0 {
		return nil, fmt.Errorf("workspace_unavailable: invalid managed process identity")
	}
	bootID, err := currentIsolatedBootID()
	if err != nil {
		return nil, err
	}
	process, err := readIsolatedProcStat(pid)
	if err != nil || process.Group != pid {
		return nil, fmt.Errorf("workspace_unavailable: gateway is not an isolated process group")
	}
	processInfo, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: gateway disappeared during recording")
	}
	owner, ok := processInfo.Sys().(*syscall.Stat_t)
	if !ok || (spec.UID > 0 && owner.Uid != uint32(spec.UID)) {
		return nil, fmt.Errorf("workspace_unavailable: gateway process owner does not match instance")
	}
	identity := isolatedProcessIdentity{Version: 1, InstanceID: spec.InstanceID, Generation: spec.Generation, PID: pid, UID: owner.Uid, BootID: bootID, StartTicks: process.StartTicks}
	store, err := openIsolatedProcessStore(cfg)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	name := isolatedRecordName(spec.InstanceID, spec.Generation)
	if _, err := store.Lstat(name); !os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace_unavailable: gateway process metadata already exists")
	}
	if err := writeIsolatedIdentity(store, name, identity); err != nil {
		return nil, err
	}
	return func() {
		current, err := openIsolatedProcessStore(cfg)
		if err != nil {
			return
		}
		defer current.Close()
		stored, err := readIsolatedIdentity(current, name)
		if err == nil && stored == identity {
			_ = current.Remove(name)
		}
	}, nil
}

func writeIsolatedIdentity(store *os.Root, name string, identity isolatedProcessIdentity) error {
	data, _ := json.Marshal(identity)
	tmpName := ".tmp-" + rand.Text()
	file, err := store.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("workspace_unavailable: create process metadata")
	}
	defer store.Remove(tmpName)
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("workspace_unavailable: write process metadata")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("workspace_unavailable: sync process metadata")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace_unavailable: close process metadata")
	}
	if err := store.Rename(tmpName, name); err != nil {
		return fmt.Errorf("workspace_unavailable: replace process metadata")
	}
	return nil
}

func readIsolatedIdentity(store *os.Root, name string) (isolatedProcessIdentity, error) {
	var identity isolatedProcessIdentity
	info, err := store.Lstat(name)
	if err != nil {
		return identity, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return identity, fmt.Errorf("workspace_unavailable: insecure process metadata")
	}
	file, err := store.Open(name)
	if err != nil {
		return identity, fmt.Errorf("workspace_unavailable: open process metadata")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return identity, fmt.Errorf("workspace_unavailable: process metadata changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &identity) != nil || identity.Version != 1 || identity.PID <= 1 || identity.StartTicks == 0 || identity.BootID == "" || identity.InstanceID <= 0 || identity.Generation <= 0 || name != isolatedRecordName(identity.InstanceID, identity.Generation) {
		return identity, fmt.Errorf("workspace_unavailable: invalid process metadata")
	}
	return identity, nil
}

func recoverIsolatedProcesses(cfg Config) error {
	store, err := openIsolatedProcessStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	bootID, err := currentIsolatedBootID()
	if err != nil {
		return err
	}
	directory, err := store.Open(".")
	if err != nil {
		return fmt.Errorf("workspace_unavailable: read process recovery directory")
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return fmt.Errorf("workspace_unavailable: read process recovery records")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		identity, err := readIsolatedIdentity(store, entry.Name())
		if err != nil {
			return err
		}
		if identity.BootID == bootID {
			if err := recoverIsolatedProcess(identity, cfg.ProcessStopTimeout); err != nil {
				return err
			}
		}
		if err := store.Remove(entry.Name()); err != nil {
			return fmt.Errorf("workspace_unavailable: remove recovered process metadata")
		}
	}
	return nil
}

func recoverIsolatedProcess(identity isolatedProcessIdentity, timeout time.Duration) error {
	current, err := readIsolatedProcStat(identity.PID)
	if os.IsNotExist(err) {
		live, scanErr := isolatedGroupAlive(identity.PID)
		if scanErr != nil || live {
			return fmt.Errorf("workspace_unavailable: cannot verify orphaned gateway process group")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace_unavailable: inspect recovered gateway process")
	}
	if current.StartTicks != identity.StartTicks {
		return nil // PID reuse: never signal an unrelated process.
	}
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(identity.PID)))
	if err != nil {
		return fmt.Errorf("workspace_unavailable: inspect recovered gateway ownership")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	group, err := syscall.Getpgid(identity.PID)
	if !ok || owner.Uid != identity.UID || err != nil || group != identity.PID || current.Group != identity.PID {
		return fmt.Errorf("workspace_unavailable: recovered gateway process identity does not match")
	}
	if err := syscall.Kill(-identity.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("workspace_unavailable: terminate recovered gateway group")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	if waitIsolatedGroupExit(identity.PID, timeout) {
		return nil
	}
	if err := syscall.Kill(-identity.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("workspace_unavailable: kill recovered gateway group")
	}
	if !waitIsolatedGroupExit(identity.PID, 5*time.Second) {
		return fmt.Errorf("workspace_unavailable: recovered gateway group did not stop")
	}
	return nil
}

func waitIsolatedGroupExit(group int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		live, err := isolatedGroupAlive(group)
		if err == nil && !live {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func isolatedGroupAlive(group int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := readIsolatedProcStat(pid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if stat.Group == group && stat.State != "Z" && stat.State != "X" {
			return true, nil
		}
	}
	return false, nil
}
