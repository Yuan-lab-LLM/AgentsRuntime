// Package sandbox builds fail-closed Linux bubblewrap commands for untrusted
// gateway processes. It does not authorize identities or select workspace paths:
// a trusted controller must derive every mount from the assigned instance.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Mount grants access to precisely one directory. Source must already exist and
// contain no symbolic-link path components. Targets must not overlap.
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// Spec contains only explicitly authorized child arguments, business environment
// and data mounts. Never populate Env with os.Environ(): controller credentials
// belong outside the sandbox. UID and GID must be nonzero.
type Spec struct {
	// TmpSource is an explicit grant for one supervisor-prepared instance tmp.
	// Empty preserves the ephemeral tmpfs used by maintenance sandboxes.
	TmpSource string
	// OfflineNetwork is for trusted maintenance with no outbound service grant.
	// Every command gets a fresh, disconnected NET rather than the Pod network.
	OfflineNetwork    bool
	NetworkHostsFile  string
	NetworkResolvConf string
	Args              []string
	Env               []string
	Dir               string
	UID, GID          int
	Mounts            []Mount
}

// These trees must contain immutable image content only. Deployment must never
// project Secrets, service-account tokens, PVC roots or controller state beneath
// them. /etc is deliberately assembled from selected public configuration.
var imagePaths = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/defaults",
	"/etc/hosts", "/etc/hostname", "/etc/resolv.conf", "/etc/nsswitch.conf",
	"/etc/passwd", "/etc/group", "/etc/ld.so.cache", "/etc/ld.so.conf",
	"/etc/ld.so.conf.d", "/etc/ssl/certs", "/etc/ca-certificates.conf",
	"/etc/localtime", "/etc/timezone", "/etc/fonts", "/etc/alternatives",
}

// Command returns argv including the absolute bubblewrap executable. Linux,
// bubblewrap and the kernel namespace setup are mandatory: no fallback exists.
//
// The caller must launch bubblewrap as the requested OUTER UID/GID (with only
// authorized supplemental groups), close all unrelated FDs, leave ExtraFiles
// empty except Track's controlled pipes, and provide a sanitized outer environment. Bubblewrap maps that host
// identity into its user namespace; launching it as host root would map sandbox
// UID to host root instead of the persistent workspace owner. Bubblewrap sets
// no_new_privs itself and --cap-drop ALL removes recoverable capabilities.
//
// Source ancestors must remain controlled by management until exec: path
// validation cannot prevent an attacker replacing a source after this function
// returns. A process's writable subtree must not include its mount's parent.
func Command(spec Spec) ([]string, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("sandbox requires Linux namespaces")
	}
	binary, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandbox requires bubblewrap: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve bubblewrap: %w", err)
	}
	var image []Mount
	for _, path := range imagePaths {
		resolved, err := filepath.EvalSymlinks(path)
		if os.IsNotExist(err) && path != "/usr" {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox image path %s: %w", path, err)
		}
		image = append(image, Mount{Source: resolved, Target: path, ReadOnly: true})
	}
	return buildCommand(spec, binary, image)
}

func buildCommand(spec Spec, binary string, image []Mount) ([]string, error) {
	if err := validate(spec); err != nil {
		return nil, err
	}
	args := []string{binary,
		"--unshare-user", "--disable-userns", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--uid", strconv.Itoa(spec.UID), "--gid", strconv.Itoa(spec.GID), "--clearenv",
	}
	if spec.OfflineNetwork {
		args = append(args, "--unshare-net")
	}
	for _, mount := range image {
		if spec.NetworkHostsFile != "" && (mount.Target == "/etc/hosts" || mount.Target == "/etc/resolv.conf") {
			continue
		}
		args = append(args, "--ro-bind", mount.Source, mount.Target)
	}
	if spec.NetworkHostsFile != "" {
		args = append(args, "--ro-bind", spec.NetworkHostsFile, "/etc/hosts", "--ro-bind", spec.NetworkResolvConf, "/etc/resolv.conf")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	if spec.TmpSource == "" {
		args = append(args, "--perms", "1777", "--tmpfs", "/tmp")
	} else {
		args = append(args, "--bind", spec.TmpSource, "/tmp")
	}
	args = append(args, "--perms", "0700", "--tmpfs", "/run",
		"--perms", "1777", "--tmpfs", "/dev/shm")
	// The outer supervisor must permit a fresh proc mount. Restore sensitive
	// kernel masks inside that new PID proc before any business process starts.
	for _, path := range []string{"/proc/acpi", "/proc/asound", "/proc/kcore", "/proc/keys", "/proc/latency_stats", "/proc/timer_list", "/proc/timer_stats", "/proc/sched_debug", "/proc/scsi"} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect sandbox proc mask %s: %w", path, err)
		}
		if info.IsDir() {
			args = append(args, "--tmpfs", path, "--remount-ro", path)
		} else {
			args = append(args, "--ro-bind", "/dev/null", path)
		}
	}
	for _, mount := range spec.Mounts {
		flag := "--bind"
		if mount.ReadOnly {
			flag = "--ro-bind"
		}
		args = append(args, flag, mount.Source, mount.Target)
	}
	for _, item := range spec.Env {
		key, value, _ := strings.Cut(item, "=")
		args = append(args, "--setenv", key, value)
	}
	// The assembled root is immutable; the separate authorized data and tmpfs
	// mounts retain their own permissions. Mount propagation is private in bwrap.
	args = append(args, "--remount-ro", "/", "--chdir", spec.Dir, "--")
	return append(args, spec.Args...), nil
}

func validate(spec Spec) error {
	if spec.TmpSource != "" {
		if err := validateTmpSource(spec.TmpSource); err != nil {
			return err
		}
	}
	if spec.OfflineNetwork && (spec.NetworkHostsFile != "" || spec.NetworkResolvConf != "") {
		return fmt.Errorf("offline maintenance cannot share business routing")
	}
	if spec.NetworkHostsFile != "" || spec.NetworkResolvConf != "" {
		if err := validateNetworkRoutingFile(spec.NetworkHostsFile, "hosts", NetworkPublicRoot, 0); err != nil {
			return err
		}
		if err := validateNetworkRoutingFile(spec.NetworkResolvConf, "resolv.conf", NetworkPublicRoot, 0); err != nil {
			return err
		}
		if filepath.Dir(spec.NetworkHostsFile) != filepath.Dir(spec.NetworkResolvConf) {
			return fmt.Errorf("routing files must share the same execution")
		}
	}
	if spec.UID <= 0 || spec.GID <= 0 || uint64(spec.UID) >= 1<<32-1 || uint64(spec.GID) >= 1<<32-1 {
		return fmt.Errorf("sandbox requires non-root valid UID/GID")
	}
	if len(spec.Args) == 0 || spec.Args[0] == "" {
		return fmt.Errorf("sandbox command is empty")
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("sandbox argument contains NUL")
		}
	}
	keys := make(map[string]bool, len(spec.Env))
	for _, item := range spec.Env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || !validEnvKey(key) || strings.ContainsRune(item, 0) || keys[key] {
			return fmt.Errorf("sandbox environment contains invalid or duplicate key")
		}
		keys[key] = true
	}
	if !canonical(spec.Dir) || spec.Dir == "/" {
		return fmt.Errorf("sandbox working directory must be canonical and absolute")
	}
	if len(spec.Mounts) == 0 {
		return fmt.Errorf("sandbox requires an explicit data mount")
	}
	dirMounted := false
	for i, mount := range spec.Mounts {
		if spec.TmpSource != "" && within("/tmp", mount.Target) {
			return fmt.Errorf("persistent tmp grant overlaps another mount")
		}
		policyMount := publicPolicyMount(mount)
		if !validTarget(mount.Target) && !policyMount {
			return fmt.Errorf("sandbox mount target is not allowed: %q", mount.Target)
		}
		if err := validateSource(mount.Source, policyMount); err != nil {
			return err
		}
		for _, previous := range spec.Mounts[:i] {
			if within(previous.Target, mount.Target) || within(mount.Target, previous.Target) {
				return fmt.Errorf("sandbox mount targets overlap: %s and %s", previous.Target, mount.Target)
			}
		}
		dirMounted = dirMounted || within(mount.Target, spec.Dir)
	}
	if !dirMounted {
		return fmt.Errorf("sandbox working directory must be inside an authorized mount")
	}
	return nil
}

func validateSource(source string, policyMount bool) error {
	if !canonical(source) || len(strings.Split(strings.TrimPrefix(source, "/"), "/")) < 2 {
		return fmt.Errorf("sandbox source must be a canonical absolute instance directory: %q", source)
	}
	if strings.HasPrefix(source, "/workspaces/") && !validTarget(source) {
		return fmt.Errorf("sandbox source must not expose a pool or user root: %q", source)
	}
	for _, forbidden := range []string{"/proc", "/dev", "/sys", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/defaults", "/run", "/var", "/root"} {
		if within(forbidden, source) && !policyMount {
			return fmt.Errorf("sandbox source is a protected path: %q", source)
		}
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolve sandbox source %q: %w", source, err)
	}
	if resolved != source {
		return fmt.Errorf("sandbox source contains a symlink: %q", source)
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat sandbox source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox source must be a directory: %q", source)
	}
	return nil
}

// The managed policy contains public routing/trust rules, never credentials.
// Its directory is mounted readonly to permit atomic policy-file replacement by
// the supervisor. No other /run source or target receives this exception.
func publicPolicyMount(mount Mount) bool {
	const path = "/run/clawmanager-proxy-policy"
	return mount.ReadOnly && mount.Source == path && mount.Target == path
}

func canonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func within(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}

func validTarget(target string) bool {
	if !canonical(target) {
		return false
	}
	if target == "/team" || within("/workspace", target) {
		return true
	}
	if strings.HasPrefix(target, "/home/") {
		return true
	}
	// Trusted short-lived commands may receive one explicitly authorized scratch
	// directory beneath the private tmpfs. Never bind the outer /tmp root.
	if strings.HasPrefix(target, "/tmp/") {
		return true
	}
	// Existing profiles use absolute instance paths and Team aliases. The
	// trusted caller authorizes their exact identity; pool/runtime/user roots
	// cannot be mounted by this component.
	if len(strings.Split(strings.TrimPrefix(target, "/"), "/")) < 4 {
		return false
	}
	for _, protected := range []string{"/proc", "/dev", "/sys", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/defaults", "/run", "/root", "/var"} {
		if within(protected, target) {
			return false
		}
	}
	return true
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, ch := range key {
		if ch != '_' && (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (i == 0 || ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}
