package receiptstorage

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const RootEnv = "CLAWMANAGER_TASK_RECEIPT_ROOT"
const DirectoryEnv = "CLAWMANAGER_TASK_RECEIPT_DIR"

type Identity struct {
	InstanceID string `json:"instance_id"`
	OwnerID    string `json:"owner_id"`
	UID        int    `json:"uid"`
	GID        int    `json:"gid"`
}

func Validate(root string, id Identity, excluded ...string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.VolumeName(root)+string(filepath.Separator) || !regexp.MustCompile("^[1-9][0-9]{0,9}$").MatchString(id.InstanceID) || !regexp.MustCompile("^[1-9][0-9]{0,9}$").MatchString(id.OwnerID) || id.UID <= 0 || id.GID <= 0 || id.UID > 2147483647 || id.GID > 2147483647 {
		return fmt.Errorf("invalid Pro receipt storage identity or root")
	}
	for _, other := range excluded {
		if other == "" {
			continue
		}
		if !filepath.IsAbs(other) {
			return fmt.Errorf("receipt storage exclusion must be absolute")
		}
		for _, pair := range [][2]string{{root, other}, {other, root}} {
			rel, err := filepath.Rel(filepath.Clean(pair[0]), filepath.Clean(pair[1]))
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("Pro receipt storage must be separate from user data")
			}
		}
	}
	return nil
}
func binding(id Identity) []byte   { raw, _ := json.Marshal(id); return raw }
func Directory(root string) string { return filepath.Join(root, "receipts") }
