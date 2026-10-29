//go:build linux

package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func isolatedRecoveryFixture(t *testing.T) (Config, GatewayStartSpec, *exec.Cmd) {
	t.Helper()
	cfg := Config{AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir(), ProcessStopTimeout: 100 * time.Millisecond}
	spec := GatewayStartSpec{InstanceID: 63, Generation: 2, UID: os.Getuid(), GID: os.Getgid()}
	command := exec.Command("sleep", "60")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
	})
	return cfg, spec, command
}

func TestIsolatedRecoveryStopsOnlyRecordedProcessGroup(t *testing.T) {
	cfg, spec, command := isolatedRecoveryFixture(t)
	_, _, other := isolatedRecoveryFixture(t)
	cleanup, err := recordIsolatedProcess(cfg, spec, command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path := filepath.Join(cfg.AgentDataDir, "processes", isolatedRecordName(spec.InstanceID, spec.Generation))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "env") || strings.Contains(string(data), "token") {
		t.Fatal("process recovery metadata contains sensitive configuration")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("process metadata permissions are not 0600")
	}
	if err := recoverIsolatedProcesses(cfg); err != nil {
		t.Fatal(err)
	}
	if alive, err := isolatedGroupAlive(command.Process.Pid); err != nil || alive {
		t.Fatalf("recorded group survived recovery: alive=%v err=%v", alive, err)
	}
	if err := syscall.Kill(other.Process.Pid, 0); err != nil {
		t.Fatal("unrelated process was stopped")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("recovered metadata not removed")
	}
}

func TestIsolatedRecoveryDoesNotKillReusedPIDOrPreviousBoot(t *testing.T) {
	for _, change := range []string{"start_ticks", "boot_id"} {
		t.Run(change, func(t *testing.T) {
			cfg, spec, command := isolatedRecoveryFixture(t)
			cleanup, err := recordIsolatedProcess(cfg, spec, command.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			store, err := openIsolatedProcessStore(cfg)
			if err != nil {
				t.Fatal(err)
			}
			name := isolatedRecordName(spec.InstanceID, spec.Generation)
			identity, err := readIsolatedIdentity(store, name)
			if err != nil {
				t.Fatal(err)
			}
			if change == "start_ticks" {
				identity.StartTicks++
			} else {
				identity.BootID = "old-boot"
			}
			if err := writeIsolatedIdentity(store, name, identity); err != nil {
				t.Fatal(err)
			}
			store.Close()
			if err := recoverIsolatedProcesses(cfg); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(command.Process.Pid, 0); err != nil {
				t.Fatal("unverified process was stopped")
			}
		})
	}
}

func TestIsolatedRecoveryRejectsMetadataSymlink(t *testing.T) {
	cfg := Config{AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir()}
	store, err := openIsolatedProcessStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	outside := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(outside, []byte("private-user-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.AgentDataDir, "processes", "63-2.json")); err != nil {
		t.Fatal(err)
	}
	if err := recoverIsolatedProcesses(cfg); err == nil {
		t.Fatal("symlink recovery metadata accepted")
	}
	after, _ := os.ReadFile(outside)
	if string(after) != "private-user-data" {
		t.Fatal("recovery touched a user file")
	}
}

func TestIsolatedRecoveryRejectsUnisolatedProcess(t *testing.T) {
	cfg := Config{AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir()}
	command := exec.Command("sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if _, err := recordIsolatedProcess(cfg, GatewayStartSpec{InstanceID: 1, Generation: 1}, command.Process.Pid); err == nil {
		t.Fatal("recorded a process that shares the agent process group")
	}
}

func TestParseIsolatedProcStatAllowsParenthesesInCommand(t *testing.T) {
	fields := []string{"S", "1", "42", "42", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "123456"}
	stat, err := parseIsolatedProcStat([]byte("42 (strange ) process (name)) " + strings.Join(fields, " ")))
	if err != nil || stat.Group != 42 || stat.StartTicks != 123456 {
		t.Fatalf("invalid parsed process stat: %#v %v", stat, err)
	}
}

func TestIsolatedRecoveryPreservesNewerRecordDuringOldCleanup(t *testing.T) {
	cfg, spec, command := isolatedRecoveryFixture(t)
	cleanup, err := recordIsolatedProcess(cfg, spec, command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openIsolatedProcessStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name := isolatedRecordName(spec.InstanceID, spec.Generation)
	identity, err := readIsolatedIdentity(store, name)
	if err != nil {
		t.Fatal(err)
	}
	identity.StartTicks++
	if err := writeIsolatedIdentity(store, name, identity); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := store.Stat(name); err != nil {
		t.Fatal("old cleanup removed newer process metadata")
	}
}

func TestIsolatedProcessStoreRejectsWorkspaceStorage(t *testing.T) {
	workspace := t.TempDir()
	_, err := openIsolatedProcessStore(Config{WorkspaceRoot: workspace, AgentDataDir: filepath.Join(workspace, "hermes", "user-"+strconv.Itoa(os.Getuid()), "processes")})
	if err == nil {
		t.Fatal("accepted user-writable workspace for control process metadata")
	}
}
