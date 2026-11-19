package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func testSpec(t *testing.T) Spec {
	t.Helper()
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Args: []string{"/bin/sh", "-c", "printf ok"}, Env: []string{"HOME=/workspace/home", "TOKEN=business=token"}, Dir: "/workspace", UID: 200001, GID: 200001, Mounts: []Mount{{Source: source, Target: "/workspace"}}}
}

func containsArgs(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func TestCommandUsesEmptyFilesystemAndExplicitEnvironment(t *testing.T) {
	spec := testSpec(t)
	t.Setenv("MANAGEMENT_SECRET", "must-not-escape")
	args, err := buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]string{
		{"--unshare-user"}, {"--unshare-pid"}, {"--unshare-ipc"}, {"--unshare-uts"},
		{"--disable-userns"}, {"--die-with-parent"}, {"--new-session"}, {"--cap-drop", "ALL"},
		{"--uid", "200001"}, {"--gid", "200001"}, {"--clearenv"},
		{"--proc", "/proc"}, {"--dev", "/dev"}, {"--tmpfs", "/tmp"}, {"--tmpfs", "/run"},
		{"--bind", spec.Mounts[0].Source, "/workspace"}, {"--chdir", "/workspace"},
		{"--setenv", "TOKEN", "business=token"},
	} {
		if !containsArgs(args, required) {
			t.Errorf("missing %q in %q", required, args)
		}
	}
	for _, denied := range []string{"--unshare-net", "--share-net", "--unshare-all", "--dev-bind", "--as-pid-1", "MANAGEMENT_SECRET", "must-not-escape"} {
		if slices.Contains(args, denied) {
			t.Errorf("unexpected argument %s", denied)
		}
	}
	if containsArgs(args, []string{"--ro-bind", "/", "/"}) {
		t.Fatal("container root exposed")
	}
	if !slices.Equal(args[len(args)-len(spec.Args)-1:], append([]string{"--"}, spec.Args...)) {
		t.Fatal("command arguments changed")
	}
}

func TestExplicitReadonlyMountAndExistingWorkspacePaths(t *testing.T) {
	for _, target := range []string{"/workspaces/openclaw/user-1/instance-2", "/data/openclaw/user-1/instance-2", "/workspaces/team-workspaces/team-1/shared", "/home/instance-2", "/team", "/tmp/operation-scratch"} {
		t.Run(target, func(t *testing.T) {
			spec := testSpec(t)
			spec.Mounts[0].Target, spec.Mounts[0].ReadOnly, spec.Dir = target, true, target
			args, err := buildCommand(spec, "/usr/bin/bwrap", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !containsArgs(args, []string{"--ro-bind", spec.Mounts[0].Source, target}) {
				t.Fatal("readonly mount not preserved")
			}
		})
	}
}

func TestRejectUnsafeMounts(t *testing.T) {
	for _, target := range []string{"", ".", "workspace", "/", "/workspace/../other", "/workspace/", "/proc", "/proc/1/root", "/dev/shm", "/etc", "/etc/token", "/usr/bin", "/opt/custom", "/tmp", "/run", "/home", "/workspaces", "/workspaces/openclaw", "/workspaces/openclaw/user-1", "/unknown/instance"} {
		t.Run("target="+target, func(t *testing.T) {
			spec := testSpec(t)
			spec.Mounts[0].Target = target
			if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
				t.Fatalf("accepted dangerous target %q", target)
			}
		})
	}
	for _, source := range []string{"", ".", "relative", "/", "/workspaces", "/data", "/home", "/etc", "/etc/ssl", "/run/secrets", "/proc/self/root", "/usr/local"} {
		t.Run("source="+source, func(t *testing.T) {
			spec := testSpec(t)
			spec.Mounts[0].Source = source
			if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
				t.Fatalf("accepted dangerous source %q", source)
			}
		})
	}
}

func TestRejectSymlinksIncludingSourceAncestors(t *testing.T) {
	spec := testSpec(t)
	root := spec.Mounts[0].Source
	if err := os.Mkdir(filepath.Join(root, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real", "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{filepath.Join(root, "link"), filepath.Join(root, "link", "child"), root + "/real/../real", filepath.Join(root, "missing")} {
		spec.Mounts[0].Source = source
		if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
			t.Errorf("accepted symlinked, noncanonical or missing source %s", source)
		}
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	spec.Mounts[0].Source = file
	if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
		t.Fatal("accepted nondirectory source")
	}
}

func TestRejectOverlappingMountTargets(t *testing.T) {
	for _, target := range []string{"/workspace", "/workspace/home"} {
		spec := testSpec(t)
		spec.Mounts = append(spec.Mounts, Mount{Source: spec.Mounts[0].Source, Target: target})
		if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
			t.Fatalf("accepted overlap at %s", target)
		}
	}
}

func TestRejectInvalidIdentityArgumentsEnvironmentAndDirectory(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Spec)
	}{
		{"root uid", func(s *Spec) { s.UID = 0 }}, {"root gid", func(s *Spec) { s.GID = 0 }},
		{"negative uid", func(s *Spec) { s.UID = -1 }}, {"empty argv", func(s *Spec) { s.Args = nil }},
		{"empty executable", func(s *Spec) { s.Args = []string{""} }}, {"nul argv", func(s *Spec) { s.Args = []string{"sh", "a\x00b"} }},
		{"malformed environment", func(s *Spec) { s.Env = []string{"TOKEN"} }}, {"empty env key", func(s *Spec) { s.Env = []string{"=value"} }},
		{"duplicate env", func(s *Spec) { s.Env = []string{"A=1", "A=2"} }}, {"nul env", func(s *Spec) { s.Env = []string{"A=x\x00y"} }},
		{"bad env key", func(s *Spec) { s.Env = []string{"-A=1"} }}, {"relative directory", func(s *Spec) { s.Dir = "workspace" }},
		{"unmounted directory", func(s *Spec) { s.Dir = "/workspaces/other/user-2/instance-3" }}, {"root directory", func(s *Spec) { s.Dir = "/" }},
		{"no mounts", func(s *Spec) { s.Mounts = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := testSpec(t)
			tc.change(&spec)
			if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
				t.Fatal("accepted invalid spec")
			}
		})
	}
}

func TestImageFilesystemAllowlist(t *testing.T) {
	spec := testSpec(t)
	image := []Mount{{Source: "/usr", Target: "/usr", ReadOnly: true}, {Source: "/etc/hosts", Target: "/etc/hosts", ReadOnly: true}}
	args, err := buildCommand(spec, "/usr/bin/bwrap", image)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range image {
		if !containsArgs(args, []string{"--ro-bind", mount.Source, mount.Target}) {
			t.Errorf("missing image mount %s", mount.Target)
		}
	}
	for _, path := range []string{"/usr", "/opt", "/defaults", "/etc/hosts", "/etc/resolv.conf", "/etc/ssl/certs"} {
		if !slices.Contains(imagePaths, path) {
			t.Errorf("missing required image path %s", path)
		}
	}
	for _, path := range []string{"/", "/etc", "/run", "/var", "/var/run", "/workspaces", "/root", "/data"} {
		if slices.Contains(imagePaths, path) {
			t.Errorf("image allowlist contains %s", path)
		}
	}
	before := append([]string(nil), spec.Env...)
	if _, err := buildCommand(spec, "/usr/bin/bwrap", image); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, spec.Env) {
		t.Fatal("builder mutated caller environment")
	}
}

func TestCommandFailsClosedWithoutLinuxOrBubblewrap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Command(testSpec(t))
	if err == nil {
		t.Fatal("accepted missing isolation support")
	}
	if runtime.GOOS != "linux" && !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("unexpected unsupported platform error: %v", err)
	}
}

func TestPublicProxyPolicyIsAnExactReadonlyMountPair(t *testing.T) {
	path := "/run/clawmanager-proxy-policy"
	for _, tc := range []struct {
		mount   Mount
		allowed bool
	}{
		{Mount{Source: path, Target: path, ReadOnly: true}, true},
		{Mount{Source: path, Target: path}, false},
		{Mount{Source: "/run/secrets", Target: path, ReadOnly: true}, false},
		{Mount{Source: path, Target: "/run/other", ReadOnly: true}, false},
		{Mount{Source: path + "/subdir", Target: path, ReadOnly: true}, false},
		{Mount{Source: path, Target: path + "/subdir", ReadOnly: true}, false},
	} {
		if got := publicPolicyMount(tc.mount); got != tc.allowed {
			t.Errorf("policy mount %+v: got %v, want %v", tc.mount, got, tc.allowed)
		}
	}
	// Even readonly request-controlled sources cannot supply a trusted policy.
	spec := testSpec(t)
	spec.Mounts = append(spec.Mounts, Mount{Source: spec.Mounts[0].Source, Target: path, ReadOnly: true})
	if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
		t.Fatal("accepted arbitrary source as trusted proxy policy")
	}
}

func TestRejectBroadWorkspaceSourcesBeforeFilesystemLookup(t *testing.T) {
	for _, source := range []string{"/workspaces/runtime", "/workspaces/runtime/user-1"} {
		if err := validateSource(source, false); err == nil || !strings.Contains(err.Error(), "pool") {
			t.Fatalf("did not reject broad PVC source %s before lookup: %v", source, err)
		}
	}
}

func TestCommandMasksSensitiveProcPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel proc entries require Linux")
	}
	args, err := buildCommand(testSpec(t), "/usr/bin/bwrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/proc/acpi", "/proc/asound", "/proc/kcore", "/proc/keys", "/proc/latency_stats", "/proc/timer_list", "/proc/timer_stats", "/proc/sched_debug", "/proc/scsi"} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"--ro-bind", "/dev/null", path}
		if info.IsDir() {
			want = []string{"--tmpfs", path, "--remount-ro", path}
		}
		if !containsArgs(args, want) {
			t.Errorf("new PID proc exposes sensitive kernel path %s", path)
		}
	}
}
