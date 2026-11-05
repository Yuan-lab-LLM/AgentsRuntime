//go:build linux

package gateway

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNFSSkillScanRejectsNonregularManifestWithoutBlocking(t *testing.T) {
	for _, name := range []string{"SKILL.md", "skill.json", "openclaw.skill.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			skill := filepath.Join(root, "skills", "private")
			if err := os.MkdirAll(skill, 0700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(skill, name), 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := ScanSkillRootsChecked(root, filepath.Join(root, "skills")); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("nonregular manifest accepted")
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("FIFO manifest blocked scan")
			}
		})
	}
}

func TestNFSSkillScanRejectsSymlinkManifest(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skills", "private")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other"), []byte("redirected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../other", filepath.Join(skill, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSkillRootsChecked(root, filepath.Join(root, "skills")); err == nil {
		t.Fatal("symlink manifest accepted")
	}
}

func TestNFSSkillScanCancellationReapsBusinessHelper(t *testing.T) {
	if os.Getenv("CM_SKILL_CANCEL_ACCEPTANCE") != "1" || os.Geteuid() != 10001 {
		t.Skip("requires management10001 with only SETUID/SETGID")
	}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		cmd := exec.CommandContext(ctx, "/bin/sleep", "30")
		configureNativeSkillScanCommand(cmd, 271001, 271001)
		started := time.Now()
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := cmd.Process.Signal(syscall.SIGWINCH); !errors.Is(err, syscall.EPERM) {
			_ = cmd.Cancel()
			cancel()
			_ = cmd.Wait()
			t.Fatalf("requires no outer KILL capability: %v", err)
		}
		err := cmd.Wait()
		cancel()
		if err == nil || time.Since(started) > time.Second {
			t.Fatalf("helper was not promptly reaped: %v", err)
		}
		if cmd.ProcessState == nil {
			t.Fatal("business helper not reaped")
		}
		if os.Geteuid() != 10001 {
			t.Fatal("helper cancellation changed manager identity")
		}
	}
}

func TestNFSSkillInputOpenRejectsSubstitutedSpecialFiles(t *testing.T) {
	base := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(base, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("fifo", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := workspaceio.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"fifo", "link"} {
		for _, directory := range []bool{true, false} {
			done := make(chan error, 1)
			go func() {
				file, err := openNativeSkillInput(root, name, directory)
				if file != nil {
					file.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("special file accepted")
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("special file open blocked")
			}
		}
	}
	file, err := openNativeSkillInput(root, ".", true)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
