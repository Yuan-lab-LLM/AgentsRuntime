package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const RootlessCommand = "network-guard"

type rootlessConfig struct {
	Config   Config
	Identity Identity
}

type rootlessReady struct {
	Parent, Child string
	Closed        bool
}

// RootlessHandle owns a trusted namespace keeper. The broker socket objects stay
// in the keeper's network namespace even when served by the outer supervisor.
type RootlessHandle struct {
	mu            sync.Mutex
	cmd           *exec.Cmd
	control       *net.UnixConn
	user, network *os.File
	listeners     []net.Listener
	udp           net.PacketConn
	parent, child string
	done          chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	waitErr       error
	diagnostic    func() string
	closed        bool
}

func (h *RootlessHandle) ParentIP() string         { return h.parent }
func (h *RootlessHandle) ChildIP() string          { return h.child }
func (h *RootlessHandle) Context() context.Context { return h.ctx }

// BrokerListeners transfers ownership in fixed order: egress, model, services,
// Redis, TCP DNS. UDP DNS is returned separately.
func (h *RootlessHandle) BrokerListeners() ([]net.Listener, net.PacketConn) {
	ls, udp := h.listeners, h.udp
	h.listeners, h.udp = nil, nil
	return ls, udp
}

func rootlessEnterArgs(userFD, netFD int, command []string) []string {
	// -p prevents BASH_ENV/ENV startup code; bash supports descriptor numbers
	// above nine when an execution has many inherited ingress listeners.
	args := []string{"--no-fork", "--user=/proc/self/fd/" + strconv.Itoa(userFD), "--net=/proc/self/fd/" + strconv.Itoa(netFD), "--", "/bin/bash", "--noprofile", "--norc", "-p", "-c", fmt.Sprintf(`exec %d<&- %d<&-; exec "$@"`, userFD, netFD), "network-enter"}
	return append(args, command...)
}

func (h *RootlessHandle) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	for _, l := range h.listeners {
		_ = l.Close()
	}
	if h.udp != nil {
		_ = h.udp.Close()
	}
	if h.control == nil {
		return fmt.Errorf("netguard: missing rootless ownership channel")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = h.control.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = h.control.Close() })
	defer stop()
	_, err := h.control.Write([]byte("close"))
	var ready rootlessReady
	if err == nil {
		buf := make([]byte, 512)
		var n int
		n, err = h.control.Read(buf)
		if err == nil && (json.Unmarshal(buf[:n], &ready) != nil || !ready.Closed) {
			err = errors.New("invalid cleanup receipt")
		}
	}
	if err != nil {
		detail := ""
		if h.diagnostic != nil {
			detail = h.diagnostic()
		}
		return fmt.Errorf("%w: rootless helper cleanup not confirmed: %v %s", ErrCleanupUnconfirmed, err, detail)
	}
	select {
	case <-h.done:
		if h.waitErr != nil {
			return fmt.Errorf("%w: rootless helper exit failed", ErrCleanupUnconfirmed)
		}
	case <-ctx.Done():
		return fmt.Errorf("%w: rootless helper still running", ErrCleanupUnconfirmed)
	}
	_ = h.control.Close()
	_ = h.user.Close()
	_ = h.network.Close()
	h.cancel()
	h.closed = true
	return nil
}
