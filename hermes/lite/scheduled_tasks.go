package hermes

import (
	"github.com/iamlovingit/clawmanager-agent/scheduledtasks"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io/fs"
	"os"
	"path/filepath"
)

// Native serializer runs in a private management staging tree. Only root-bounded
// file publication touches the instance; user symlinks are never serializer paths.
func applyHermesTasks(root *workspaceio.FS, home string, getenv func(string) string, uid, gid int) scheduledtasks.ApplyResult {
	_, raw := scheduledtasks.ReadScheduledTasksEnv(getenv)
	if raw == "" {
		return scheduledtasks.ApplyResult{Skipped: true}
	}
	staging, err := os.MkdirTemp("", "cm-hermes-tasks-")
	if err != nil {
		return scheduledtasks.ApplyResult{Error: err.Error()}
	}
	defer os.RemoveAll(staging)
	for _, name := range []string{"jobs.json", "clawmanager-scheduled-tasks.sha256"} {
		data, err := root.ReadFile(filepath.Join(home, "cron", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return scheduledtasks.ApplyResult{Error: err.Error()}
		}
		if err := os.MkdirAll(filepath.Join(staging, "cron"), 0700); err != nil {
			return scheduledtasks.ApplyResult{Error: err.Error()}
		}
		if err := os.WriteFile(filepath.Join(staging, "cron", name), data, 0600); err != nil {
			return scheduledtasks.ApplyResult{Error: err.Error()}
		}
	}
	result := scheduledtasks.ApplyHermesFromEnv(staging, getenv, 0, 0)
	if result.Error != "" {
		return result
	}
	err = filepath.WalkDir(staging, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(staging, path)
		if err != nil {
			return err
		}
		target := filepath.Join(home, relative)
		if entry.IsDir() {
			return root.MkdirAll(target, 0750)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fs.ErrInvalid
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := root.WriteFileAtomic(target, data, 0640); err != nil {
			return err
		}
		return root.Chown(target, uid, gid)
	})
	if err != nil {
		result.Error = err.Error()
	}
	result.StorePath = filepath.Join(home, "cron", "jobs.json")
	return result
}
