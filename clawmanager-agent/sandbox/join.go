package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// JoinTerminal uses supervisor-held namespace descriptors from the current
// primary. The terminal gets a distinct mount namespace for complete, selective
// descendant cleanup; authorized image/workspace/tmp mounts and process/network
// view match the primary.
// No PID or namespace path comes from the workbench request.
func (t *Tracker) JoinTerminal(cmd *exec.Cmd, spec Spec, groups []uint32) (_ *Tracker, err error) {
	if runtime.GOOS != "linux" || t == nil || cmd == nil || cmd.Process != nil || cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil || len(cmd.ExtraFiles) != 0 {
		return nil, fmt.Errorf("terminal requires an unstarted scoped Linux command")
	}
	cred := cmd.SysProcAttr.Credential
	if spec.UID < 200000 || uint32(spec.UID) != t.businessUID || cred.Uid != uint32(spec.UID) || cred.Gid != uint32(spec.GID) || cred.NoSetGroups || !slices.Equal(cred.Groups, groups) {
		return nil, fmt.Errorf("terminal execution identity does not match primary")
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	t.namespaceMu.Lock()
	defer t.namespaceMu.Unlock()
	primary, err := t.primaryGateway()
	if err != nil {
		return nil, fmt.Errorf("primary execution is unavailable: %w", err)
	}
	dir := filepath.Join(t.procRoot, strconv.Itoa(primary.PID))
	status, err := readWorkbenchProc(filepath.Join(dir, "status"), 64<<10)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			matched = len(fields) == 5
			for _, value := range fields[1:] {
				matched = matched && value == strconv.Itoa(spec.UID)
			}
		}
	}
	if !matched {
		return nil, fmt.Errorf("primary execution UID changed")
	}
	var opened []*os.File
	defer func() {
		if err != nil {
			for _, file := range opened {
				file.Close()
			}
		}
	}()
	for _, name := range []string{"ns/user", "ns/pid", "ns/ipc", "ns/uts", "ns/net"} {
		file, e := os.Open(filepath.Join(dir, name))
		if e != nil {
			return nil, fmt.Errorf("pin primary execution %s: %w", name, e)
		}
		opened = append(opened, file)
	}
	owner, err := namespaceOwner(opened[1], opened[0], t.containerLifecycle)
	if err != nil {
		return nil, err
	}
	opened = append([]*os.File{owner}, opened...)
	current, err := t.readIdentity(primary.PID)
	alive, aliveErr := t.initAlive()
	if err != nil || aliveErr != nil || !alive || current != primary {
		return nil, fmt.Errorf("primary changed while pinning terminal execution")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, err
	}
	bwrap, err = filepath.Abs(bwrap)
	if err != nil {
		return nil, err
	}
	spec.joinExecution = true
	// Protected networking grants the trusted supervisor namespace capabilities.
	// Legacy non-networked profiles retain fresh IPC/UTS isolation and the same
	// existing Pod network; their PID join needs only the owned user namespace.
	spec.joinNamespaces = spec.NetworkHostsFile != ""
	spec.ContainerLifecycle = t.containerLifecycle
	args, err := Command(spec)
	if err != nil {
		return nil, err
	}
	cmd.Path, cmd.Args = bwrap, args
	tracker, err := TrackContainer(cmd)
	if err != nil {
		return nil, err
	}
	tracker.auxiliaryMountScope = true
	tracker.joinFiles = opened
	cmd.ExtraFiles = append(cmd.ExtraFiles, opened...)
	if !spec.joinNamespaces {
		return tracker, nil
	}
	priv := []string{"/usr/bin/setpriv", "--reuid", strconv.Itoa(spec.UID), "--regid", strconv.Itoa(spec.GID)}
	if len(groups) == 0 {
		priv = append(priv, "--clear-groups")
	} else {
		values := make([]string, len(groups))
		for i, group := range groups {
			values[i] = strconv.FormatUint(uint64(group), 10)
		}
		priv = append(priv, "--groups", strings.Join(values, ","))
	}
	priv = append(priv, "--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--")
	args = []string{"/usr/bin/nsenter", "--no-fork", "--ipc=/proc/self/fd/9", "--uts=/proc/self/fd/10", "--net=/proc/self/fd/11", "--"}
	args = append(args, priv...)
	cmd.Path, cmd.Args = args[0], append(args, cmd.Args...)
	attr := *cmd.SysProcAttr
	attr.Credential = nil
	cmd.SysProcAttr = &attr
	return tracker, nil
}
