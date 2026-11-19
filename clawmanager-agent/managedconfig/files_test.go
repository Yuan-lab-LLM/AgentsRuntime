//go:build !windows

package managedconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestManagedFilesBoundedAtomicRewrite(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "instance")
	home := filepath.Join(workspace, "home", ".native")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(home, "config.json")
	if err := os.WriteFile(name, []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	selectFile := func(name string) Transform {
		if name == "config.json" {
			return func(data []byte) ([]byte, error) {
				return []byte(strings.ReplaceAll(string(data), "old", "local")), nil
			}
		}
		return nil
	}
	if err := Rewrite(context.Background(), base, workspace, []Directory{{"home/.native", selectFile}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	info, _ := os.Stat(name)
	if string(data) != "local" || info.Mode().Perm() != 0640 {
		t.Fatalf("content/mode %q %v", data, info.Mode())
	}
	if err := Rewrite(context.Background(), base, filepath.Join(base, "new"), []Directory{{"home/.native", selectFile}}); err != nil {
		t.Fatal(err)
	}
}
func TestManagedFilesRejectUnsafeBeforeRewrite(t *testing.T) {
	for _, scenario := range []string{"symlink", "hardlink", "fifo", "directory", "parent_symlink", "oversize", "entries", "files", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			workspace := filepath.Join(base, "instance")
			home := filepath.Join(workspace, "home", ".native")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			safe := filepath.Join(home, "a.json")
			os.WriteFile(safe, []byte("old"), 0600)
			bad := filepath.Join(home, "z.json")
			switch scenario {
			case "symlink":
				os.Symlink(safe, bad)
			case "hardlink":
				os.Link(safe, bad)
			case "fifo":
				if err := syscall.Mkfifo(bad, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Mkdir(bad, 0700)
			case "parent_symlink":
				os.Rename(home, home+"-other")
				os.Symlink(home+"-other", home)
			case "oversize":
				f, _ := os.Create(bad)
				f.Truncate(MaxFileBytes + 1)
				f.Close()
			case "entries":
				for i := 0; i <= MaxEntries; i++ {
					os.WriteFile(filepath.Join(home, fmt.Sprint(i)), nil, 0600)
				}
			case "files":
				for i := 0; i <= MaxFiles; i++ {
					os.WriteFile(filepath.Join(home, fmt.Sprintf("%d.json", i)), nil, 0600)
				}
			case "malformed":
				os.WriteFile(bad, []byte("bad"), 0600)
			}
			selector := func(name string) Transform {
				if !strings.HasSuffix(name, ".json") {
					return nil
				}
				return func(b []byte) ([]byte, error) {
					if string(b) == "bad" {
						return nil, fmt.Errorf("invalid")
					}
					return []byte("local"), nil
				}
			}
			if err := Rewrite(context.Background(), base, workspace, []Directory{{"home/.native", selector}}); err == nil {
				t.Fatal("unsafe managed files accepted")
			}
			data, _ := os.ReadFile(safe)
			if string(data) != "old" {
				t.Fatal("rewrote before completing validation")
			}
		})
	}
}

func TestManagedFilesCreateWithoutFollowingExistingAlias(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "instance")
	home := filepath.Join(workspace, "home", ".native")
	os.MkdirAll(home, 0700)
	if err := Create(context.Background(), base, workspace, "home/.native", "config.json", []byte("new"), 0600, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := Create(context.Background(), base, workspace, "home/.native", "config.json", []byte("overwrite"), 0600, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("create overwrote existing credential file")
	}
	data, _ := os.ReadFile(filepath.Join(home, "config.json"))
	if string(data) != "new" {
		t.Fatal("existing file changed")
	}
}

func TestManagedSubdirectoriesBoundedAndPinned(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "instance")
	agents := filepath.Join(workspace, "home", ".native", "agents")
	os.MkdirAll(filepath.Join(agents, "main"), 0700)
	dirs, err := Subdirectories(context.Background(), base, workspace, "home/.native/agents")
	if err != nil || len(dirs) != 1 || dirs[0] != "main" {
		t.Fatalf("directory result %v %v", dirs, err)
	}
	os.Symlink(filepath.Join(agents, "main"), filepath.Join(agents, "alias"))
	if _, err = Subdirectories(context.Background(), base, workspace, "home/.native/agents"); err == nil {
		t.Fatal("accepted aliased generated agent directory")
	}
}
