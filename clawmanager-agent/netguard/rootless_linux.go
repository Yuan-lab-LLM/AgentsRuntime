//go:build linux

package netguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/iamlovingit/clawmanager-agent/sandboxprofile"
)

func rootlessMappings(uid, gid int, groups []uint32) (string, string, error) {
	valid := func(id int) bool {
		return id > 0 && id != sandboxprofile.ManagementUID && id != 65534 && uint64(id) < 1<<32-1
	}
	if uid < 200000 || !valid(uid) || !valid(gid) {
		return "", "", fmt.Errorf("netguard: invalid rootless business identity")
	}
	base := fmt.Sprintf("0 %d 1\n65534 65534 1\n", sandboxprofile.ManagementUID)
	uids, gids := base+fmt.Sprintf("%d %d 1\n", uid, uid), base+fmt.Sprintf("%d %d 1\n", gid, gid)
	for i, g := range groups {
		if !valid(int(g)) || slices.Contains(groups[:i], g) {
			return "", "", fmt.Errorf("netguard: invalid rootless supplemental identity")
		}
		if int(g) != gid {
			gids += fmt.Sprintf("%d %d 1\n", g, g)
		}
	}
	return uids, gids, nil
}

// PrepareRootless uses bubblewrap's executable AppArmor allowance and externally
// supplied identity maps. Neither its launcher nor the business gets outer
// SYS_ADMIN/NET_ADMIN. The trusted caller must already be management UID.
func PrepareRootless(ctx context.Context, cfg Config, id Identity, uid, gid int, groups []uint32, executable string) (result *RootlessHandle, resultErr error) {
	if os.Geteuid() != sandboxprofile.ManagementUID || os.Getegid() != sandboxprofile.ManagementGID {
		return nil, fmt.Errorf("netguard: rootless supervisor must use management identity")
	}
	uids, gids, err := rootlessMappings(uid, gid, groups)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("netguard: invalid helper executable")
	}
	expected, err := newHandle(cfg, id, runCommand)
	if err != nil {
		return nil, err
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parentFile, childFile := os.NewFile(uintptr(pair[0]), "network-control"), os.NewFile(uintptr(pair[1]), "network-control-child")
	defer parentFile.Close()
	defer childFile.Close()
	connection, err := net.FileConn(parentFile)
	if err != nil {
		return nil, err
	}
	control := connection.(*net.UnixConn)
	infoR, infoW, err := os.Pipe()
	if err != nil {
		control.Close()
		return nil, err
	}
	defer infoR.Close()
	defer infoW.Close()
	blockR, blockW, err := os.Pipe()
	if err != nil {
		control.Close()
		return nil, err
	}
	defer blockR.Close()
	defer blockW.Close()
	helperCtx, cancel := context.WithCancel(context.Background())
	h := &RootlessHandle{control: control, done: make(chan struct{}), ctx: helperCtx, cancel: cancel, parent: expected.ParentIP(), child: expected.ChildIP()}
	var diagnostics rootlessDiagnostics
	h.diagnostic = diagnostics.String
	defer func() {
		if resultErr != nil {
			if detail := diagnostics.String(); detail != "" {
				resultErr = fmt.Errorf("%w; trusted namespace helper: %s", resultErr, detail)
			}
		}
	}()
	// Dropping inherited/ambient capabilities only for the bwrap child prevents
	// its nonroot startup guard rejecting the supervisor's SETUID/SETGID caps.
	cmd := exec.Command(setprivTool, "--inh-caps=-all", "--ambient-caps=-all", "--", "/usr/bin/bwrap", "--unshare-user", "--unshare-net", "--uid", "0", "--gid", "0", "--info-fd", "3", "--userns-block-fd", "4", "--bind", "/", "/", "--tmpfs", "/run", "--dir", "/run/netns", "--dir", "/run/clawmanager-network-state", "--cap-add", "ALL", "--", executable, RootlessCommand)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	data, _ := json.Marshal(rootlessConfig{Config: cfg, Identity: id})
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = io.Discard, &diagnostics
	cmd.ExtraFiles = []*os.File{infoW, blockR, childFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	h.cmd = cmd
	if err = cmd.Start(); err != nil {
		control.Close()
		cancel()
		return nil, err
	}
	go func() { h.waitErr = cmd.Wait(); close(h.done); cancel() }()
	failed := true
	defer func() {
		if failed {
			// Fail closed and retain uncertainty: killing a monitor is never proof
			// that its same-container descendant or a writer has stopped.
			_ = control.Close()
			_ = cmd.Process.Kill()
		}
	}()
	_ = infoW.Close()
	_ = blockR.Close()
	_ = childFile.Close()
	setupCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	interrupt := context.AfterFunc(setupCtx, func() { _ = infoR.Close(); _ = control.Close() })
	defer interrupt()
	var info struct {
		ChildPID int `json:"child-pid"`
	}
	if err = json.NewDecoder(io.LimitReader(infoR, 4096)).Decode(&info); err != nil || info.ChildPID <= 0 {
		return h, fmt.Errorf("%w: rootless namespace mapping rendezvous failed", ErrCleanupUnconfirmed)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", info.ChildPID))
	if err != nil {
		return h, fmt.Errorf("%w: rootless child identity unavailable", ErrCleanupUnconfirmed)
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return h, fmt.Errorf("%w: invalid rootless child identity", ErrCleanupUnconfirmed)
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 2 || fields[1] != strconv.Itoa(cmd.Process.Pid) {
		return h, fmt.Errorf("%w: rootless child ownership mismatch", ErrCleanupUnconfirmed)
	}
	for name, mapping := range map[string]string{"uid_map": uids, "gid_map": gids} {
		if err = os.WriteFile(fmt.Sprintf("/proc/%d/%s", info.ChildPID, name), []byte(mapping), 0600); err != nil {
			return h, fmt.Errorf("%w: rootless %s mapping failed: %v", ErrCleanupUnconfirmed, name, err)
		}
	}
	_ = blockW.Close()
	if deadline, ok := setupCtx.Deadline(); ok {
		_ = control.SetReadDeadline(deadline)
	}
	if err = h.receive(); err != nil {
		return h, fmt.Errorf("%w: %v", ErrCleanupUnconfirmed, err)
	}
	_ = control.SetDeadline(time.Time{})
	failed = false
	return h, nil
}

// The fixed namespace keeper receives network/UID metadata only, never broker
// credentials. Bound its setup/cleanup diagnostics without exposing child env.
type rootlessDiagnostics struct {
	mu   sync.Mutex
	data []byte
}

func (d *rootlessDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	remaining := 512 - len(d.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		d.data = append(d.data, p[:remaining]...)
	}
	return n, nil
}
func (d *rootlessDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.TrimSpace(string(d.data))
}

func (h *RootlessHandle) receive() error {
	data, ancillary := make([]byte, 1024), make([]byte, syscall.CmsgSpace(8*4))
	n, oobn, flags, _, err := h.control.ReadMsgUnix(data, ancillary)
	if err != nil {
		return err
	}
	messages, err := syscall.ParseSocketControlMessage(ancillary[:oobn])
	var fds []int
	if err == nil {
		for _, message := range messages {
			rights, e := syscall.ParseUnixRights(&message)
			if e != nil {
				err = e
				break
			}
			fds = append(fds, rights...)
		}
	}
	defer func() {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	}()
	if err != nil || flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || len(messages) != 1 || len(fds) != 8 {
		return fmt.Errorf("netguard: invalid rootless descriptor bundle")
	}
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
	}
	var ready rootlessReady
	d := json.NewDecoder(bytes.NewReader(data[:n]))
	d.DisallowUnknownFields()
	if d.Decode(&ready) != nil || d.Decode(new(any)) != io.EOF || ready.Closed || net.ParseIP(ready.Parent) == nil || net.ParseIP(ready.Child) == nil {
		return fmt.Errorf("netguard: invalid rootless readiness")
	}
	if ready.Parent != h.parent || ready.Child != h.child {
		return fmt.Errorf("netguard: rootless address assignment mismatch")
	}
	if err = validateRootlessNamespaces(fds[6], fds[7]); err != nil {
		return err
	}
	var listeners []net.Listener
	var packet net.PacketConn
	ok := false
	defer func() {
		if !ok {
			for _, l := range listeners {
				l.Close()
			}
			if packet != nil {
				packet.Close()
			}
		}
	}()
	for i, port := range []int{31280, 31281, 31282, 31283, 53} {
		f := os.NewFile(uintptr(fds[i]), "broker-listener")
		l, e := net.FileListener(f)
		_ = f.Close()
		fds[i] = -1
		if e != nil {
			return fmt.Errorf("netguard: invalid broker listener")
		}
		listeners = append(listeners, l)
		if l.Addr().Network() != "tcp" || l.Addr().String() != net.JoinHostPort(ready.Parent, strconv.Itoa(port)) {
			return fmt.Errorf("netguard: broker endpoint mismatch")
		}
	}
	f := os.NewFile(uintptr(fds[5]), "dns-packet")
	packet, err = net.FilePacketConn(f)
	_ = f.Close()
	fds[5] = -1
	if err != nil || packet.LocalAddr().Network() != "udp" || packet.LocalAddr().String() != net.JoinHostPort(ready.Parent, "53") {
		return fmt.Errorf("netguard: DNS endpoint mismatch")
	}
	h.user, h.network = os.NewFile(uintptr(fds[6]), "execution-userns"), os.NewFile(uintptr(fds[7]), "execution-netns")
	fds[6], fds[7] = -1, -1
	h.parent, h.child, h.listeners, h.udp = ready.Parent, ready.Child, listeners, packet
	ok = true
	return nil
}

func validateRootlessNamespaces(userFD, netFD int) error {
	ioctl := func(fd int, op uintptr, arg uintptr) (uintptr, error) {
		r, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), op, arg)
		if e != 0 {
			return r, e
		}
		return r, nil
	}
	for _, entry := range []struct{ fd, kind int }{{userFD, syscall.CLONE_NEWUSER}, {netFD, syscall.CLONE_NEWNET}} {
		kind, err := ioctl(entry.fd, 0xb703, 0)
		if err != nil || int(kind) != entry.kind {
			return fmt.Errorf("netguard: invalid namespace descriptor type")
		}
	}
	var owner uint32
	if _, err := ioctl(userFD, 0xb704, uintptr(unsafe.Pointer(&owner))); err != nil || owner != sandboxprofile.ManagementUID {
		return fmt.Errorf("netguard: invalid namespace owner")
	}
	owned, err := ioctl(netFD, 0xb701, 0)
	if err != nil {
		return fmt.Errorf("netguard: network owner unavailable")
	}
	defer syscall.Close(int(owned))
	var expected, actual syscall.Stat_t
	if syscall.Fstat(userFD, &expected) != nil || syscall.Fstat(int(owned), &actual) != nil || expected.Dev != actual.Dev || expected.Ino != actual.Ino {
		return fmt.Errorf("netguard: network namespace ownership mismatch")
	}
	return nil
}

func (h *RootlessHandle) Wrap(cmd *exec.Cmd, uid, gid int, groups []uint32) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil || h.user == nil || h.network == nil {
		return fmt.Errorf("netguard: rootless namespace unavailable")
	}
	// Reuse legacy argument/credential/Track-FD validation before replacing the
	// namespace wrapper with the pinned rootless user+network pair.
	guard := &Handle{ready: true, name: "validated-rootless"}
	if err := guard.wrap(cmd, uid, gid, groups); err != nil {
		return err
	}
	command := cmd.Args[4:]
	cmd.Args = append([]string{nsenterTool}, rootlessEnterArgs(6, 7, command)...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, h.user, h.network)
	return nil
}

func (h *RootlessHandle) HelperCommand(ctx context.Context, files []*os.File, argv ...string) (*exec.Cmd, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil || h.user == nil || h.network == nil || len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, fmt.Errorf("netguard: rootless helper unavailable")
	}
	command := append([]string{setprivTool}, privilegeArgs(65534, 65534, nil)...)
	command = append(command, argv...)
	cmd := exec.CommandContext(ctx, nsenterTool, rootlessEnterArgs(3+len(files), 4+len(files), command)...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = append(append([]*os.File(nil), files...), h.user, h.network)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

func RunRootlessCommand(ctx context.Context, args []string, input io.Reader, ready io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != RootlessCommand {
		return false, nil
	}
	if len(args) != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return true, fmt.Errorf("netguard: invalid namespace helper identity")
	}
	var cfg rootlessConfig
	d := json.NewDecoder(io.LimitReader(input, 32769))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF {
		return true, fmt.Errorf("netguard: invalid namespace helper configuration")
	}
	// These paths are mounted as fresh tmpfs by our fixed bwrap invocation.
	// General Prepare retains its stricter arbitrary-path ancestor validation.
	cfg.Config.StateDir = "/run/clawmanager-network-state"
	info, err := os.Lstat(cfg.Config.StateDir)
	if err != nil || !info.IsDir() || !ownedBy(info, 0) {
		return true, fmt.Errorf("netguard: missing private helper state")
	}
	if err = os.Chmod(cfg.Config.StateDir, 0700); err != nil {
		return true, err
	}
	h, err := newHandle(cfg.Config, cfg.Identity, runCommand)
	if err != nil {
		return true, err
	}
	if err = h.prepare(ctx); err != nil {
		return true, err
	}
	defer h.Close(context.Background())
	f := os.NewFile(5, "network-control")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return true, err
	}
	control, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return true, fmt.Errorf("netguard: invalid control socket")
	}
	defer control.Close()
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for _, port := range []int{31280, 31281, 31282, 31283, 53} {
		l, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP(h.ParentIP()), Port: port})
		if e != nil {
			return true, e
		}
		f, e := l.File()
		l.Close()
		if e != nil {
			return true, e
		}
		files = append(files, f)
	}
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(h.ParentIP()), Port: 53})
	if err != nil {
		return true, err
	}
	f, err = u.File()
	u.Close()
	if err != nil {
		return true, err
	}
	files = append(files, f)
	for _, path := range []string{"/proc/self/ns/user", h.NamespacePath()} {
		f, err = os.Open(path)
		if err != nil {
			return true, err
		}
		files = append(files, f)
	}
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	data, _ := json.Marshal(rootlessReady{Parent: h.ParentIP(), Child: h.ChildIP()})
	if _, _, err = control.WriteMsgUnix(data, syscall.UnixRights(fds...), nil); err != nil {
		return true, err
	}
	for _, f := range files {
		f.Close()
	}
	files = nil
	stop := context.AfterFunc(ctx, func() { control.Close() })
	defer stop()
	buffer := make([]byte, 32)
	n, err := control.Read(buffer)
	if err != nil || string(buffer[:n]) != "close" {
		return true, errors.New("netguard: control channel lost before cleanup")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err = h.Close(cleanupCtx); err != nil {
		return true, err
	}
	data, _ = json.Marshal(rootlessReady{Closed: true})
	_, err = control.Write(data)
	return true, err
}
