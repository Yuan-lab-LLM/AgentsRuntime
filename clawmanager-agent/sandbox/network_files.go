package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// NetworkPublicRoot contains public routing information only. Management network
// state and namespace handles must never be made traversable to business UIDs.
const NetworkPublicRoot = "/run/clawmanager-network-public"

var networkDirectoryName = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validateNetworkRoutingFile(source, name, root string, owner uint32) error {
	if !canonical(source) || filepath.Base(source) != name || filepath.Dir(filepath.Dir(source)) != root || !networkDirectoryName.MatchString(filepath.Base(filepath.Dir(source))) {
		return fmt.Errorf("invalid public network routing path")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || resolved != source {
		return fmt.Errorf("public network routing path contains symlink or is unavailable")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0444 || !networkFileOwner(info, owner) {
		return fmt.Errorf("public network routing file must be root-owned regular 0444")
	}
	// All ancestors must be controlled. Public root and execution directory must
	// also be readable/traversable after nsenter drops the business UID.
	for path := filepath.Dir(source); ; path = filepath.Dir(path) {
		info, err = os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !networkFileOwner(info, owner) {
			return fmt.Errorf("public network routing ancestor is unsafe")
		}
		if (path == root || path == filepath.Dir(source)) && info.Mode().Perm() != 0755 {
			return fmt.Errorf("public network routing directories require 0755")
		}
		if path == "/" || (path == root && owner != 0) {
			break
		}
	}
	return nil
}
