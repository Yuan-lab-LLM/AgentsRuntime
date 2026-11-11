package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcessIdentity pins a Linux PID to its start time in clock ticks since boot.
// Persisted identities must also be associated with the current kernel boot ID.
type ProcessIdentity struct {
	PID       int
	StartTime uint64
}

// Tracker records the namespace init process before allowing the gateway to run.
// Waiting for the outer bubblewrap monitor alone does not prove child teardown.
// Started and Close must not run concurrently. WaitStopped is safe after Started.
type Tracker struct {
	containerLifecycle           bool
	auxiliaryMountScope          bool
	joinFiles                    []*os.File
	businessUID                  uint32
	cmd                          *exec.Cmd
	infoR, infoW, blockR, blockW *os.File
	filter                       *os.File
	identity                     ProcessIdentity
	captured, released           bool
	procRoot                     string
	namespaceMu                  sync.Mutex
	namespaceFile                *os.File
	namespaceInfo                os.FileInfo
	networkFile                  *os.File
	networkInfo                  os.FileInfo
}

// Track attaches controlled lifecycle pipes and a syscall policy before cmd.Start.
// No unrelated ExtraFiles are permitted. Bubblewrap closes these FDs before exec.
// Call Started after cmd.Start, Close on every error path, and WaitStopped before
// acknowledging stop or releasing a writer lease. Close does not prove teardown.
func Track(cmd *exec.Cmd) (*Tracker, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("sandbox process tracking requires Linux")
	}
	return newTracker(cmd)
}

// ErrContainerResetRequired prevents primary-process exit from being treated
// as proof that detached descendants in the shared slot PID namespace ended.
var ErrContainerResetRequired = errors.New("slot container termination proof is required")

func TrackContainer(cmd *exec.Cmd) (*Tracker, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("sandbox process tracking requires Linux")
	}
	return newTrackerMode(cmd, true)
}

func newTracker(cmd *exec.Cmd) (*Tracker, error) {
	return newTrackerMode(cmd, false)
}

func newTrackerMode(cmd *exec.Cmd, containerLifecycle bool) (*Tracker, error) {
	if cmd == nil || len(cmd.Args) < 2 || cmd.Process != nil || len(cmd.ExtraFiles) != 0 {
		return nil, fmt.Errorf("sandbox tracking requires an unstarted command without inherited FDs")
	}
	pidNamespace := false
	for _, arg := range cmd.Args[1:] {
		if arg == "--" {
			break
		}
		if arg == "--unshare-pid" {
			pidNamespace = true
		}
		if arg == "--info-fd" || arg == "--block-fd" || arg == "--as-pid-1" || arg == "--seccomp" || arg == "--add-seccomp-fd" {
			return nil, fmt.Errorf("sandbox command has incompatible lifecycle options")
		}
	}
	if containerLifecycle && pidNamespace {
		return nil, fmt.Errorf("container tracking must reuse the slot PID namespace")
	}
	if !containerLifecycle && !pidNamespace {
		return nil, fmt.Errorf("sandbox tracking requires a private PID namespace")
	}
	filter, err := seccompFilter()
	if err != nil {
		return nil, err
	}
	infoR, infoW, err := os.Pipe()
	if err != nil {
		filter.Close()
		return nil, fmt.Errorf("create sandbox identity pipe: %w", err)
	}
	blockR, blockW, err := os.Pipe()
	if err != nil {
		filter.Close()
		_ = infoR.Close()
		_ = infoW.Close()
		return nil, fmt.Errorf("create sandbox startup pipe: %w", err)
	}
	tracker := &Tracker{containerLifecycle: containerLifecycle, cmd: cmd, infoR: infoR, infoW: infoW, blockR: blockR, blockW: blockW, filter: filter, procRoot: "/proc"}
	tracker.businessUID = processLaunchUID(cmd)
	cmd.ExtraFiles = []*os.File{infoW, blockR, filter}
	cmd.Args = append([]string{cmd.Args[0], "--info-fd", "3", "--block-fd", "4", "--seccomp", "5"}, cmd.Args[1:]...)
	return tracker, nil
}

// Started captures PID1's identity while the gateway is still blocked. Failure
// leaves the gateway blocked until Close kills the monitor. Such a failure must
// keep the slot quarantined unless teardown can be independently verified.
func (t *Tracker) Started(ctx context.Context) error {
	if t.cmd == nil || t.cmd.Process == nil || t.captured {
		return fmt.Errorf("sandbox tracker is not awaiting startup")
	}
	_ = t.infoW.Close()
	_ = t.blockR.Close()
	if t.filter != nil {
		_ = t.filter.Close()
	}
	type info struct {
		ChildPID int `json:"child-pid"`
	}
	type result struct {
		data info
		err  error
	}
	decoded := make(chan result, 1)
	go func() {
		var data info
		err := json.NewDecoder(io.LimitReader(t.infoR, 4096)).Decode(&data)
		decoded <- result{data, err}
	}()
	var r result
	select {
	case r = <-decoded:
	case <-ctx.Done():
		_ = t.infoR.Close()
		return fmt.Errorf("wait for sandbox identity: %w", ctx.Err())
	}
	_ = t.infoR.Close()
	if r.err != nil {
		return fmt.Errorf("read sandbox identity: %w", r.err)
	}
	if err := t.capture(r.data.ChildPID, t.cmd.Process.Pid); err != nil {
		return err
	}
	// EOF releases bubblewrap's block-fd only after its trusted identity is held.
	if err := t.blockW.Close(); err != nil {
		return fmt.Errorf("release sandbox startup: %w", err)
	}
	t.released = true
	return nil
}

func (t *Tracker) capture(pid, parentPID int) error {
	if pid <= 0 {
		return fmt.Errorf("sandbox reported invalid namespace init PID")
	}
	data, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return fmt.Errorf("read sandbox init identity: %w", err)
	}
	identity, state, ppid, err := parseProcessIdentity(pid, data)
	if err != nil {
		return err
	}
	parentMatches := ppid == parentPID
	if !parentMatches && t.auxiliaryMountScope {
		// --pidns uses one trusted intermediate fork. The final child stays
		// blocked on our startup FD; verify that fork's exact monitor ancestry.
		// The intermediate may already be an unreaped zombie. Its PID cannot
		// be reused yet; do not accept an unrelated/reparented Pod init.
		raw, parentErr := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(ppid), "stat"))
		intermediate, intermediateState, monitor, parseErr := parseProcessIdentity(ppid, raw)
		parentMatches = parentErr == nil && parseErr == nil && monitor == parentPID && intermediateState != "X" && intermediate.StartTime <= identity.StartTime
	}
	if !parentMatches && t.auxiliaryMountScope {
		// The intermediate can be reaped between the two stat reads. Bwrap
		// is a subreaper: accept one fresh, unchanged child only if the exact
		// monitor now owns it. Reparenting to a Pod init grants no authority.
		raw, readErr := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(pid), "stat"))
		current, currentState, currentParent, parseErr := parseProcessIdentity(pid, raw)
		parentMatches = readErr == nil && parseErr == nil && current == identity && currentParent == parentPID && currentState != "Z" && currentState != "X"
	}
	if !parentMatches || state == "Z" || state == "X" {
		return fmt.Errorf("sandbox init is not a live child of its monitor (pid=%d ppid=%d monitor=%d state=%s)", pid, ppid, parentPID, state)
	}
	status, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return fmt.Errorf("read sandbox init namespace: %w", err)
	}
	isInit := false
	uidCaptured := !t.containerLifecycle
	for _, line := range strings.Split(string(status), "\n") {
		if t.containerLifecycle && strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 {
				return fmt.Errorf("invalid sandbox outer identity")
			}
			uid, err := strconv.ParseUint(fields[1], 10, 32)
			if err != nil || uid == 0 || uid == 1<<32-1 || fields[1] != fields[2] || fields[1] != fields[3] || fields[1] != fields[4] {
				return fmt.Errorf("sandbox outer identity is not a fixed business UID")
			}
			if t.businessUID != 0 && t.businessUID != uint32(uid) {
				return fmt.Errorf("sandbox outer business UID changed during startup")
			}
			t.businessUID = uint32(uid)
			uidCaptured = true
		}
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			isInit = len(fields) >= 3 && fields[len(fields)-1] == "1"
		}
	}
	if !uidCaptured {
		return fmt.Errorf("sandbox outer business UID was not captured")
	}
	if !isInit && !t.containerLifecycle {
		return fmt.Errorf("sandbox child is not a private PID namespace init")
	}
	t.identity, t.captured = identity, true
	return nil
}

// WaitStopped waits until the original namespace PID1 has exited. Linux kills
// and waits for every other process in a PID namespace before its PID1 becomes
// a zombie. PID reuse is checked using the recorded start time, never kill(0).
// Errors and timeouts are uncertain teardown and must prevent slot reuse.
func (t *Tracker) WaitStopped(ctx context.Context) error {
	if t.containerLifecycle {
		return ErrContainerResetRequired
	}
	if !t.captured {
		return fmt.Errorf("sandbox namespace identity was not captured")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "stat"))
		if namespaceInitDisappeared(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("verify sandbox init exit: %w", err)
		}
		current, state, _, err := parseProcessIdentity(t.identity.PID, data)
		if err != nil {
			return err
		}
		if current.StartTime != t.identity.StartTime || state == "Z" || state == "X" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox namespace still alive: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// namespaceInitDisappeared applies only to the already captured PID1 read in
// WaitStopped, never to startup capture or namespace ownership checks.
func namespaceInitDisappeared(err error) bool {
	// Linux proc_single_show returns ESRCH when the task exits after the proc
	// entry was opened. Unwrap only ReadFile's direct PathError: mixed/unknown
	// errors must not turn a permission or IO failure into confirmed teardown.
	if pathErr, ok := err.(*os.PathError); ok {
		err = pathErr.Err
	}
	return os.IsNotExist(err) || err == syscall.ESRCH
}

var errGatewayPending = errors.New("sandbox primary gateway is not yet visible")

// Terminate sends SIGTERM to the primary gateway, rather than the outer monitor
// (whose death immediately kills the PID namespace). It only signals a pinned
// Linux process handle after validating the namespace PID2/start identity. No
// numeric PID fallback is allowed. The caller must still allow its shutdown
// grace period, force-kill the monitor if necessary, then require WaitStopped.
// A kernel without Go's pidfd support cannot provide graceful pinned signaling.
func (t *Tracker) Terminate(ctx context.Context) error {
	if !t.captured {
		return fmt.Errorf("sandbox namespace identity was not captured")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("sandbox gateway signaling requires Linux pidfds")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity, err := t.primaryGateway()
		if errors.Is(err, errGatewayPending) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if err != nil {
			return err
		}
		process, err := os.FindProcess(identity.PID)
		if err != nil {
			return fmt.Errorf("pin primary gateway: %w", err)
		}
		// FindProcess can silently fall back to numeric PIDs. WithHandle makes
		// that downgrade an explicit failure instead of risking PID reuse.
		if err = process.WithHandle(func(uintptr) {}); err != nil {
			_ = process.Release()
			return fmt.Errorf("pinned gateway signaling unavailable: %w", err)
		}
		current, err := t.readIdentity(identity.PID)
		if err == nil && current != identity {
			err = fmt.Errorf("primary gateway identity changed while pinning")
		}
		if err == nil {
			var alive bool
			alive, err = t.initAlive()
			if !alive && err == nil {
				err = os.ErrProcessDone
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			if t.containerLifecycle {
				err = SignalProcess(process, t.businessUID, syscall.SIGTERM)
			} else {
				err = process.Signal(syscall.SIGTERM)
			}
		}
		_ = process.Release()
		return err
	}
}

func (t *Tracker) initAlive() (bool, error) {
	data, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "stat"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	identity, state, _, err := parseProcessIdentity(t.identity.PID, data)
	return err == nil && identity == t.identity && state != "Z" && state != "X", err
}

func (t *Tracker) readIdentity(pid int) (ProcessIdentity, error) {
	data, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return ProcessIdentity{}, err
	}
	identity, _, _, err := parseProcessIdentity(pid, data)
	return identity, err
}

func (t *Tracker) primaryGateway() (ProcessIdentity, error) {
	alive, err := t.initAlive()
	if err != nil {
		return ProcessIdentity{}, err
	}
	if !alive {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	if t.containerLifecycle {
		return t.identity, nil
	}
	children, err := os.ReadFile(filepath.Join(t.procRoot, strconv.Itoa(t.identity.PID), "task", strconv.Itoa(t.identity.PID), "children"))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read sandbox init children: %w", err)
	}
	for _, raw := range strings.Fields(string(children)) {
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 0 {
			return ProcessIdentity{}, fmt.Errorf("invalid sandbox child PID")
		}
		data, err := os.ReadFile(filepath.Join(t.procRoot, raw, "stat"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ProcessIdentity{}, err
		}
		identity, state, ppid, err := parseProcessIdentity(pid, data)
		if err != nil {
			return ProcessIdentity{}, err
		}
		if ppid != t.identity.PID || state == "Z" || state == "X" {
			continue
		}
		status, err := os.ReadFile(filepath.Join(t.procRoot, raw, "status"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ProcessIdentity{}, err
		}
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "NSpid:") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[len(fields)-1] == "2" {
					return identity, nil
				}
			}
		}
	}
	return ProcessIdentity{}, errGatewayPending
}

// Close releases local pipe resources. Before successful startup it first kills
// the monitor, so releasing the startup pipe cannot intentionally launch a job
// after a failed identity check. Call WaitStopped separately when identity exists.
func (t *Tracker) Close() error {
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	for _, file := range t.joinFiles {
		_ = file.Close()
	}
	t.joinFiles = nil
	if t.namespaceFile != nil {
		_ = t.namespaceFile.Close()
		t.namespaceFile = nil
	}
	if t.networkFile != nil {
		_ = t.networkFile.Close()
		t.networkFile = nil
	}
	if !t.released && t.cmd != nil && t.cmd.Process != nil {
		var err error
		if t.containerLifecycle {
			err = SignalProcess(t.cmd.Process, t.businessUID, syscall.SIGKILL)
		} else {
			err = t.cmd.Process.Kill()
		}
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("failed startup process remains fenced: %w", err)
		}
	}
	for _, file := range []*os.File{t.infoR, t.infoW, t.blockR, t.blockW, t.filter} {
		if file != nil {
			_ = file.Close()
		}
	}
	return nil
}

func parseProcessIdentity(pid int, data []byte) (ProcessIdentity, string, int, error) {
	bad := func() (ProcessIdentity, string, int, error) {
		return ProcessIdentity{}, "", 0, fmt.Errorf("invalid Linux process identity for PID %d", pid)
	}
	text := string(data)
	left, right := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if left < 1 || right <= left {
		return bad()
	}
	statPID, err := strconv.Atoi(strings.TrimSpace(text[:left]))
	if err != nil || statPID != pid {
		return bad()
	}
	fields := strings.Fields(text[right+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return bad()
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return bad()
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return bad()
	}
	return ProcessIdentity{PID: pid, StartTime: start}, fields[0], ppid, nil
}
