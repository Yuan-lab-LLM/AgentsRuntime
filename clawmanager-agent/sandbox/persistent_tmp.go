package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func validateTmpSource(source string) error {
	if !canonical(source) {
		return fmt.Errorf("persistent tmp source must be canonical and absolute")
	}
	parts := strings.Split(filepath.ToSlash(source), "/")
	if len(parts) < 7 {
		return fmt.Errorf("persistent tmp requires an exact instance leaf")
	}
	tail := parts[len(parts)-5:]
	if tail[0] != ".clawmanager-instance-tmp" || tail[4] != "tmp" {
		return fmt.Errorf("persistent tmp requires an exact instance leaf")
	}
	switch tail[1] {
	case "openclaw", "hermes", "opencode", "deepseek-harness":
	default:
		return fmt.Errorf("invalid persistent tmp runtime")
	}
	for i, prefix := range []string{"user-", "instance-"} {
		id, err := strconv.Atoi(strings.TrimPrefix(tail[i+2], prefix))
		if err != nil || id < 1 || tail[i+2] != prefix+strconv.Itoa(id) {
			return fmt.Errorf("invalid persistent tmp identity")
		}
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if resolved != source {
		return fmt.Errorf("persistent tmp source contains a symlink")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("persistent tmp source must be a directory")
	}
	return nil
}
