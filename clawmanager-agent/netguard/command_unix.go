//go:build linux || darwin

package netguard

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func privilegeArgs(uid, gid int, groups []uint32) []string {
	args := []string{"--reuid", strconv.Itoa(uid), "--regid", strconv.Itoa(gid)}
	if len(groups) == 0 {
		args = append(args, "--clear-groups")
	} else {
		values := make([]string, len(groups))
		for i, g := range groups {
			values[i] = strconv.FormatUint(uint64(g), 10)
		}
		args = append(args, "--groups", strings.Join(values, ","))
	}
	return append(args, "--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--")
}

func (h *Handle) wrap(cmd *exec.Cmd, uid, gid int, groups []uint32) error {
	if h == nil {
		return fmt.Errorf("netguard: missing network handle")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready || cmd == nil || cmd.Process != nil || cmd.Err != nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return fmt.Errorf("netguard: wrapping requires a ready namespace and unstarted tracked command")
	}
	if uid < 200000 || uint64(uid) >= 1<<32-1 || gid <= 0 || gid == 65534 || uint64(gid) >= 1<<32-1 {
		return fmt.Errorf("netguard: invalid business identity")
	}
	cred := cmd.SysProcAttr.Credential
	if cred == nil || cred.Uid != uint32(uid) || cred.Gid != uint32(gid) || cred.NoSetGroups || !slices.Equal(cred.Groups, groups) {
		return fmt.Errorf("netguard: business credentials do not match explicit original identity")
	}
	for i, g := range groups {
		if g == 0 || g == 65534 || g == 1<<32-1 || slices.Contains(groups[:i], g) {
			return fmt.Errorf("netguard: invalid supplemental group")
		}
	}
	if !filepath.IsAbs(cmd.Path) || filepath.Clean(cmd.Path) != cmd.Path || filepath.Base(cmd.Path) != "bwrap" || len(cmd.Args) < 8 || cmd.Args[0] != cmd.Path || !slices.Equal(cmd.Args[1:7], []string{"--info-fd", "3", "--block-fd", "4", "--seccomp", "5"}) || len(cmd.ExtraFiles) != 3 {
		return fmt.Errorf("netguard: command must be the original bubblewrap with Track FDs 3/4/5")
	}
	for _, f := range cmd.ExtraFiles {
		if f == nil {
			return fmt.Errorf("netguard: missing Track descriptor")
		}
	}
	args := []string{nsenterTool, "--no-fork", "--net=" + h.NamespacePath(), "--", setprivTool}
	args = append(args, privilegeArgs(uid, gid, groups)...)
	args = append(args, cmd.Args...)
	// Only the network namespace is entered. nsenter's --no-fork and setpriv's
	// exec preserve the process identity and Track's descriptor numbering.
	cmd.Path = nsenterTool
	cmd.Args = args
	attr := *cmd.SysProcAttr
	attr.Credential = nil
	cmd.SysProcAttr = &attr
	return nil
}

func (h *Handle) helperCommand(ctx context.Context, argv ...string) (*exec.Cmd, error) {
	if h == nil {
		return nil, fmt.Errorf("netguard: missing network handle")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready || len(argv) == 0 || !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] || strings.IndexByte(argv[0], 0) >= 0 {
		return nil, fmt.Errorf("netguard: helper requires a ready namespace and fixed absolute executable")
	}
	args := []string{"--no-fork", "--net=" + h.NamespacePath(), "--", setprivTool}
	args = append(args, privilegeArgs(65534, 65534, nil)...)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, nsenterTool, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}
