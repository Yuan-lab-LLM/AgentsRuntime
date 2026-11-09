// Package netguard owns one execution's network namespace and its restrictive
// network policy. Callers must stop and join all business/helper processes before
// Close; network cleanup is not proof that a business writer has stopped.
package netguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ipTool         = "/usr/sbin/ip"
	nftTool        = "/usr/sbin/nft"
	nsenterTool    = "/usr/bin/nsenter"
	setprivTool    = "/usr/bin/setpriv"
	cleanupTimeout = 10 * time.Second
)

type Config struct {
	SlotIndex, SlotCount int
	StateDir, PodUID     string
}

type Identity struct{ InstanceID, Generation int }

type commandRunner func(context.Context, string, string, ...string) ([]byte, error)

// Handle must not be copied. Its namespace and state paths belong exclusively to
// this execution; neither may be shared with business-controlled writable paths.
type Handle struct {
	mu                                                       sync.Mutex
	run                                                      commandRunner
	name, host, peer, table                                  string
	parent, child, child6                                    string
	statePath                                                string
	ready, namespaceOwned, linkOwned, tableOwned, stateOwned bool
	namespaceAttempted, linkAttempted, tableAttempted        bool
	ownerMarker                                              string
	parentPID                                                int
	namespaceInfo                                            os.FileInfo
	statNamespace                                            func() (os.FileInfo, error)
}

var safePodUID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ErrCleanupUnconfirmed requires the caller to quarantine the failed execution.
// Ordinary setup errors with confirmed rollback do not retain owned resources.
var ErrCleanupUnconfirmed = errors.New("netguard: preparation cleanup unconfirmed")

func newHandle(cfg Config, id Identity, run commandRunner) (*Handle, error) {
	if cfg.SlotCount < 1 || cfg.SlotCount > 256 || cfg.SlotIndex < 0 || cfg.SlotIndex >= cfg.SlotCount {
		return nil, fmt.Errorf("netguard: slot index/count outside reserved /22 address budget")
	}
	if !safePodUID.MatchString(cfg.PodUID) || id.InstanceID <= 0 || id.Generation <= 0 {
		return nil, fmt.Errorf("netguard: invalid Pod UID or execution identity")
	}
	if !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir || cfg.StateDir == "/" {
		return nil, fmt.Errorf("netguard: state directory must be canonical and absolute")
	}
	var nonce [7]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("netguard: random identity: %w", err)
	}
	token := hex.EncodeToString(nonce[:])
	var owner [32]byte
	if _, err := rand.Read(owner[:]); err != nil {
		return nil, fmt.Errorf("netguard: random ownership marker: %w", err)
	}
	offset := 4 * cfg.SlotIndex
	h := &Handle{run: run, name: "n" + token, host: "h" + token, peer: "c" + token, table: "t" + token,
		parent:    fmt.Sprintf("169.254.%d.%d", 64+offset/256, offset%256+1),
		child:     fmt.Sprintf("169.254.%d.%d", 64+offset/256, offset%256+2),
		child6:    fmt.Sprintf("fd%s:%s:%s:%x::2/126", token[:2], token[2:6], token[6:10], cfg.SlotIndex),
		statePath: filepath.Join(cfg.StateDir, "n"+token), ownerMarker: "cm-netguard:" + hex.EncodeToString(owner[:]), parentPID: os.Getpid()}
	h.statNamespace = func() (os.FileInfo, error) { return os.Lstat(h.NamespacePath()) }
	return h, nil
}

// Prepare fails closed on unsupported systems or any setup/cleanup error.
// StateDir must already be a private root-owned directory; preparation never
// adopts an existing per-execution state directory.
func Prepare(ctx context.Context, cfg Config, id Identity) (*Handle, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("netguard: network isolation requires Linux")
	}
	h, err := newHandle(cfg, id, runCommand)
	if err != nil {
		return nil, err
	}
	if err = privateDirectory(cfg.StateDir, 0); err != nil {
		return nil, err
	}
	if err = h.prepare(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

func runCommand(ctx context.Context, input, tool string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("netguard: %s %v: %w: %s", tool, args, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func privateDirectory(path string, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("netguard: noncanonical state directory")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("netguard: inspect state directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("netguard: state directory contains symlink or nondirectory: %s", p)
		}
		if p == path && (info.Mode().Perm()&0077 != 0 || !ownedBy(info, owner)) {
			return fmt.Errorf("netguard: state directory must be private and owned by UID %d", owner)
		}
		if p != path && ((!ownedBy(info, owner) && !ownedBy(info, 0)) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("netguard: untrusted state directory ancestor: %s", p)
		}
		if p == "/" {
			break
		}
	}
	return nil
}

func (h *Handle) NamespacePath() string { return filepath.Join("/run/netns", h.name) }
func (h *Handle) ParentIP() string      { return h.parent }
func (h *Handle) ChildIP() string       { return h.child }
func (h *Handle) ResolvConf() string    { return filepath.Join(h.statePath, "resolv.conf") }

// Wrap enters the namespace as the trusted supervisor, then drops to the exact
// already configured business credentials before execing the tracked bubblewrap.
// It adds no descriptors and must be called after sandbox.Track and before Start.
func (h *Handle) Wrap(cmd *exec.Cmd, uid, gid int, groups []uint32) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("netguard: network isolation requires Linux")
	}
	return h.wrap(cmd, uid, gid, groups)
}

// HelperCommand accepts only root-controlled helper argv, never business input.
// Trusted callers may add explicitly controlled ExtraFiles before starting it.
func (h *Handle) HelperCommand(ctx context.Context, argv ...string) (*exec.Cmd, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("netguard: network isolation requires Linux")
	}
	return h.helperCommand(ctx, argv...)
}

func (h *Handle) execute(ctx context.Context, input, tool string, args ...string) error {
	_, err := h.run(ctx, input, tool, args...)
	return err
}
func (h *Handle) enter(ctx context.Context, input, tool string, args ...string) error {
	argv := append([]string{"--no-fork", "--net=" + h.NamespacePath(), "--", tool}, args...)
	return h.execute(ctx, input, nsenterTool, argv...)
}

func (h *Handle) prepare(ctx context.Context) (err error) {
	if err = os.Mkdir(h.statePath, 0700); err != nil {
		return fmt.Errorf("netguard: reserve execution state: %w", err)
	}
	h.stateOwned = true
	defer func() {
		if err != nil {
			if cleanupErr := h.Close(context.Background()); cleanupErr != nil {
				err = errors.Join(err, ErrCleanupUnconfirmed, cleanupErr)
			}
		}
	}()
	if err = h.persistOwnership(); err != nil {
		return err
	}
	if err = h.checkAvailable(ctx); err != nil {
		return err
	}
	h.namespaceAttempted = true
	if err = h.persistOwnership(); err != nil {
		return err
	}
	if err = h.execute(ctx, "", ipTool, "netns", "add", h.name); err != nil {
		return err
	}
	if h.namespaceInfo, err = h.statNamespace(); err != nil {
		return err
	}
	h.namespaceOwned = true
	h.linkAttempted = true
	if err = h.persistOwnership(); err != nil {
		return err
	}
	// Create both endpoints inside the already owned namespace: older kernels
	// can ignore creation aliases, so an uncertain untagged pair stays contained.
	if err = h.enter(ctx, "", ipTool, "link", "add", h.host, "type", "veth", "peer", "name", h.peer); err != nil {
		return err
	}
	for _, endpoint := range []string{h.host, h.peer} {
		if err = h.enter(ctx, "", ipTool, "link", "set", endpoint, "alias", h.ownerMarker); err != nil {
			return err
		}
	}
	if err = h.verifyContainedLinks(ctx); err != nil {
		return err
	}
	// The target PID comes exclusively from this trusted supervisor process.
	if err = h.enter(ctx, "", ipTool, "link", "set", h.host, "netns", strconv.Itoa(h.parentPID)); err != nil {
		return err
	}
	h.linkOwned = true
	if err = h.execute(ctx, "", ipTool, "addr", "add", h.parent+"/30", "dev", h.host); err != nil {
		return err
	}
	// Reject parent/forward traffic before either veth endpoint is brought up.
	h.tableAttempted = true
	if err = h.persistOwnership(); err != nil {
		return err
	}
	if err = h.execute(ctx, h.parentPolicy(), nftTool, "-f", "-"); err != nil {
		return err
	}
	h.tableOwned = true
	for _, args := range [][]string{
		{"link", "set", "lo", "up"}, {"link", "set", h.peer, "name", "eth0"},
		{"addr", "add", h.child + "/30", "dev", "eth0"},
		{"-6", "addr", "add", h.child6, "dev", "eth0", "nodad"},
	} {
		if err = h.enter(ctx, "", ipTool, args...); err != nil {
			return err
		}
	}
	if err = h.enter(ctx, h.childPolicy(), nftTool, "-f", "-"); err != nil {
		return err
	}
	if err = h.execute(ctx, "", ipTool, "link", "set", h.host, "up"); err != nil {
		return err
	}
	for _, args := range [][]string{{"link", "set", "eth0", "up"}, {"route", "add", "default", "via", h.parent}, {"-6", "route", "add", "default", "dev", "eth0"}} {
		if err = h.enter(ctx, "", ipTool, args...); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(h.ResolvConf(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "nameserver %s\noptions timeout:2 attempts:2\n", h.parent)
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err = h.persistOwnership(); err != nil {
		return err
	}
	h.ready = true
	return nil
}

func (h *Handle) parentPolicy() string {
	return fmt.Sprintf(`create table inet %s
table inet %s {
 chain %s { }
 chain input { type filter hook input priority -50; policy accept;
  iifname "%s" ip saddr %s ip daddr %s tcp dport { 53, 31280, 31281, 31282, 31283 } accept
  iifname "%s" ip saddr %s ip daddr %s udp dport 53 accept
  iifname "%s" reject
 }
 chain forward { type filter hook forward priority -50; policy accept;
  iifname "%s" reject
 }
}
`, h.table, h.table, h.ownerChain(), h.host, h.child, h.parent, h.host, h.child, h.parent, h.host, h.host)
}

func (h *Handle) childPolicy() string {
	return fmt.Sprintf(`table inet %s {
 chain tcp_redirect { type nat hook output priority -100; policy accept;
  ct direction reply return
  ip daddr 127.0.0.0/8 return
  ip6 daddr ::1 return
  ip daddr %s tcp dport { 53, 31280, 31281, 31282, 31283 } return
  meta skuid >= 200000 meta l4proto tcp redirect to :15000
 }
 chain output { type filter hook output priority 0; policy drop;
  oifname "lo" accept
  ip daddr 127.0.0.0/8 accept
  ip6 daddr ::1 accept
  ip daddr %s tcp dport { 53, 31280, 31281, 31282, 31283 } accept
  ip daddr %s udp dport 53 accept
  reject
 }
}
`, h.table, h.parent, h.parent, h.parent)
}

// Close attempts every owned cleanup operation, retaining failed ownership flags
// for a later retry. A timeout does not prove that processes have stopped.
func (h *Handle) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = false
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	var errs []error
	for _, cleanup := range []func(context.Context) error{h.cleanupLink, h.cleanupTable, h.cleanupNamespace} {
		if err := cleanup(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if h.stateOwned {
		if err := os.Remove(h.ResolvConf()); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if !h.linkOwned && !h.tableOwned && !h.namespaceOwned && !h.linkAttempted && !h.tableAttempted && !h.namespaceAttempted {
			if err := os.Remove(filepath.Join(h.statePath, "ownership.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			if err := os.Remove(h.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			} else {
				h.stateOwned = false
			}
		} else if err := h.persistOwnership(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
