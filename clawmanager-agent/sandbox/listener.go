package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// VerifyListeners attributes the primary listener and every currently listening
// socket in its assigned port block to this sandbox's recorded PID namespace.
// Optional browser/tool listeners need not exist, but any present must be owned.
// It reads the pinned init's network tables, which may differ from the trusted
// supervisor's namespace. Network policy enforcement remains a separate layer.
// The trusted supervisor needs permission to inspect descendant /proc FDs across
// user namespaces (CAP_SYS_PTRACE); user processes retain no such capability.
func (t *Tracker) VerifyListeners(ctx context.Context, first, count int) error {
	if !t.captured || first < 1 || count < 1 || count > 65536-first {
		return fmt.Errorf("invalid sandbox listener identity or port block")
	}
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	alive, err := t.initAlive()
	if err != nil || !alive {
		return fmt.Errorf("sandbox namespace init is unavailable")
	}
	if t.namespaceFile == nil {
		file, err := os.Open(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "ns", "pid"))
		if err != nil {
			return fmt.Errorf("pin sandbox PID namespace: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return err
		}
		// Holding this namespace FD prevents inode reuse. It is opened after
		// command exec and is never inherited by the gateway.
		t.namespaceFile, t.namespaceInfo = file, info
	}
	if err := t.pinListenerNetwork(); err != nil {
		return err
	}
	listeners, primary, err := t.businessListeners(first, count)
	if err != nil {
		return err
	}
	if !primary {
		return fmt.Errorf("primary gateway listener is not present")
	}
	entries, err := os.ReadDir(t.procRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 || !entry.IsDir() {
			continue
		}
		before, err := t.readIdentity(pid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(t.procRoot, entry.Name(), "ns", "pid"))
		if err != nil || !os.SameFile(t.namespaceInfo, info) {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(t.procRoot, entry.Name(), "fd"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect sandbox listener descriptors: %w", err)
		}
		owned := map[string]bool{}
		for _, fd := range fds {
			if err := ctx.Err(); err != nil {
				return err
			}
			target, err := os.Readlink(filepath.Join(t.procRoot, entry.Name(), "fd", fd.Name()))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("attribute sandbox listener descriptor: %w", err)
			}
			if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
				if _, ok := listeners[inode]; ok {
					owned[inode] = true
				}
			}
		}
		after, err := t.readIdentity(pid)
		if err != nil || before != after {
			continue
		}
		for inode := range owned {
			listeners[inode] = true
		}
	}
	for _, owned := range listeners {
		if !owned {
			return fmt.Errorf("business listener does not belong to the recorded sandbox namespace")
		}
	}
	current, primary, err := t.businessListeners(first, count)
	if err != nil {
		return err
	}
	if !primary || len(current) != len(listeners) {
		return fmt.Errorf("business listeners changed during ownership inspection")
	}
	for inode := range current {
		if !listeners[inode] {
			return fmt.Errorf("business listener identity changed during inspection")
		}
	}
	alive, err = t.initAlive()
	if err != nil || !alive {
		return fmt.Errorf("sandbox namespace changed during listener inspection")
	}
	if err := t.pinListenerNetwork(); err != nil {
		return err
	}
	return nil
}

func (t *Tracker) pinListenerNetwork() error {
	path := filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "ns", "net")
	if t.networkFile == nil {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("pin business network namespace: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		t.networkFile, t.networkInfo = file, info
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(t.networkInfo, current) {
		return fmt.Errorf("business network namespace changed")
	}
	return nil
}

func (t *Tracker) businessListeners(first, count int) (map[string]bool, bool, error) {
	listeners := map[string]bool{}
	primary := false
	for _, name := range []string{"tcp", "tcp6"} {
		file, err := os.Open(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "net", name))
		if os.IsNotExist(err) && name == "tcp6" {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("read business listeners: %w", err)
		}
		inodes, found, err := collectBusinessListeners(file, first, count)
		_ = file.Close()
		if err != nil {
			return nil, false, err
		}
		primary = primary || found
		for inode := range inodes {
			listeners[inode] = false
		}
	}
	return listeners, primary, nil
}

func collectBusinessListeners(reader io.Reader, first, count int) (map[string]bool, bool, error) {
	inodes := map[string]bool{}
	primary := false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		if len(fields) < 10 {
			return nil, false, fmt.Errorf("incomplete business listener table")
		}
		_, portText, ok := strings.Cut(fields[1], ":")
		port, err := strconv.ParseUint(portText, 16, 16)
		if !ok || err != nil {
			return nil, false, fmt.Errorf("invalid business listener port")
		}
		if int(port) < first || int(port) >= first+count {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			return nil, false, fmt.Errorf("invalid business listener inode")
		}
		inodes[fields[9]] = false
		primary = primary || int(port) == first
	}
	return inodes, primary, scanner.Err()
}
