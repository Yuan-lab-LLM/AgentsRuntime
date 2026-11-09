//go:build linux

package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// VerifyListener ties successful HTTP/WS readiness to the process group that
// the real starter created. Test starters need not implement this hook.
func (s *ExecProcessStarter) VerifyListener(spec GatewayStartSpec, pid int) error {
	failure := func() error {
		return fmt.Errorf("port_conflict: managed listener ownership could not be verified")
	}
	// A caller-controlled UID must never authorize inspection of a different
	// instance. Verify the actual process owner before using its credentials.
	if spec.UID <= 0 || spec.GID <= 0 || pid <= 1 || spec.Port < 1 || spec.Port > 65535 || !listenerProcessOwnedBy(pid, spec.UID) {
		return failure()
	}
	before, err := readListenerProcessIdentity(pid)
	if err != nil || before.group != pid {
		return failure()
	}
	if err := validateIsolatedListener(pid, spec.Port); err != nil {
		// Normal container root lacks CAP_SYS_PTRACE, so Linux may deny it
		// readlink on a different UID's /proc/<pid>/fd. Inspect as that same
		// instance UID instead of granting capabilities or weakening readiness
		// to mere TCP liveness. This fixed, read-only subcommand opens no port.
		executable, err := os.Executable()
		if err != nil || !filepath.IsAbs(executable) {
			return failure()
		}
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return failure()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "--verify-hermes-listener", strconv.Itoa(pid), strconv.Itoa(spec.Port))
		command.Env = []string{"LANG=C"}
		command.Dir = "/"
		command.Stdin = nil
		command.Stdout, command.Stderr = io.Discard, io.Discard
		command.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(spec.UID), Gid: uint32(spec.GID)},
		}
		if err := command.Run(); err != nil {
			return failure()
		}
	}
	// Preserve the identity across the fallback process lifetime, not only
	// across an individual /proc scan, and reject PID reuse or owner changes.
	after, err := readListenerProcessIdentity(pid)
	if err != nil || after.group != before.group || after.started != before.started ||
		after.state == "Z" || after.state == "X" || !listenerProcessOwnedBy(pid, spec.UID) {
		return failure()
	}
	return nil
}

func listenerProcessOwnedBy(pid, uid int) bool {
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(owner.Uid) == uint64(uid)
}

type listenerProcessIdentity struct {
	group   int
	started string
	state   string
}

func readListenerProcessIdentity(pid int) (listenerProcessIdentity, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return listenerProcessIdentity{}, err
	}
	// comm may itself contain spaces and ')', so the last ')' closes it.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return listenerProcessIdentity{}, fmt.Errorf("invalid process metadata")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return listenerProcessIdentity{}, fmt.Errorf("incomplete process metadata")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return listenerProcessIdentity{}, err
	}
	return listenerProcessIdentity{group: group, started: fields[19], state: fields[0]}, nil
}

func validateIsolatedListener(pid, port int) error {
	failure := func() error {
		return fmt.Errorf("port_conflict: listening socket does not belong to the managed process group")
	}
	if pid < 1 || port < 1 || port > 65535 {
		return failure()
	}
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid {
		return failure()
	}
	leader, err := readListenerProcessIdentity(pid)
	if err != nil || leader.group != pid || leader.state == "Z" || leader.state == "X" {
		return failure()
	}
	inodes := map[string]bool{}
	for _, name := range []string{"tcp", "tcp6"} {
		file, err := os.Open(filepath.Join("/proc/net", name))
		if os.IsNotExist(err) && name == "tcp6" {
			continue
		}
		if err != nil {
			return failure()
		}
		err = collectListenerInodes(bufio.NewScanner(file), port, inodes)
		file.Close()
		if err != nil {
			return failure()
		}
	}
	if len(inodes) == 0 {
		return failure()
	}
	markOwnedListenerInodes(pid, inodes)
	if !allListenerInodesOwned(inodes) {
		// Some supported launchers delegate the listener to a child in the
		// same managed process group. Never accept an unrelated group's FD.
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return failure()
		}
		for _, entry := range entries {
			candidate, err := strconv.Atoi(entry.Name())
			if err != nil || candidate == pid || !entry.IsDir() {
				continue
			}
			identity, err := readListenerProcessIdentity(candidate)
			if err != nil || identity.group != pid {
				continue
			}
			markOwnedListenerInodes(candidate, inodes)
			if allListenerInodesOwned(inodes) {
				break
			}
		}
	}
	if !allListenerInodesOwned(inodes) {
		return failure()
	}
	// A PID reused while inspecting /proc must not validate the predecessor's
	// socket. The lifecycle supervisor also watches this child's Done signal.
	current, err := readListenerProcessIdentity(pid)
	if err != nil || current.group != leader.group || current.started != leader.started || current.state == "Z" || current.state == "X" {
		return failure()
	}
	return nil
}

func collectListenerInodes(scanner *bufio.Scanner, port int, inodes map[string]bool) error {
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		_, rawPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			return fmt.Errorf("invalid socket metadata")
		}
		localPort, err := strconv.ParseUint(rawPort, 16, 16)
		if err != nil {
			return fmt.Errorf("invalid socket port")
		}
		if int(localPort) != port {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			return fmt.Errorf("invalid socket identity")
		}
		inodes[fields[9]] = false
	}
	return scanner.Err()
}

func markOwnedListenerInodes(pid int, inodes map[string]bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode := target[len("socket:[") : len(target)-1]
		if _, exists := inodes[inode]; exists {
			inodes[inode] = true
		}
	}
}

func allListenerInodesOwned(inodes map[string]bool) bool {
	for _, owned := range inodes {
		if !owned {
			return false
		}
	}
	return len(inodes) > 0
}
