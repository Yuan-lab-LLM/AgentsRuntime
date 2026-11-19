//go:build linux

package opencode

import (
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func singleNativePayloadIDMap(raw []byte, actor int) bool {
	fields := strings.Fields(string(raw))
	id := strconv.Itoa(actor)
	return len(fields) == 3 && fields[0] == id && (fields[1] == id || fields[1] == "0") && fields[2] == "1"
}

func validateNativePayloadNamespace(uid, gid int, proof *gateway.NativeUpgradeParentProof, roots [2]string) error {
	if uid < 200001 || uid != gid || os.Geteuid() != uid || os.Getegid() != gid {
		return errors.New("OpenCode scoped payload requires the exact business actor")
	}
	if proof == nil || proof.ActorUID != uid || proof.ActorGID != gid || len(proof.Roots) != 2 || roots[0] == "" || roots[0] == roots[1] {
		return errors.New("OpenCode scoped payload requires trusted parent grant proof")
	}
	for path, actor := range map[string]int{"/proc/self/uid_map": uid, "/proc/self/gid_map": gid} {
		raw, err := os.ReadFile(path)
		if err != nil || !singleNativePayloadIDMap(raw, actor) {
			return errors.New("OpenCode scoped payload requires the exact single identity mapping")
		}
	}
	for i, root := range proof.Roots {
		if root.Path != roots[i] || (root.OwnerUID != uint32(uid) && root.OwnerUID != 10001) || root.OwnerGID != uint32(gid) || root.Mode&0002 != 0 {
			return errors.New("OpenCode scoped parent grant identity is invalid")
		}
		info, err := os.Lstat(root.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("OpenCode scoped grant root is unavailable")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		expected := root.OwnerUID
		if expected == 10001 {
			expected = 65534
		}
		if !ok || uint64(st.Dev) != root.Device || st.Ino != root.Inode || st.Uid != expected || st.Gid != root.OwnerGID || uint32(st.Mode) != root.Mode {
			return errors.New("OpenCode scoped grant root differs from the audited inode")
		}
	}
	return nil
}

func scopedNativePayloadChange(path string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	// In the exact single-business-ID namespace, image ownership is unmapped.
	// This is NOT proof of outer root ownership: startup provides that proof;
	// fixed content hashes and read-only mounts bind the scoped view to the image.
	if !ok || stat.Uid != 65534 || stat.Gid != 65534 || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return "", errors.New("OpenCode scoped native payload is not immutable image content")
	}
	var fs syscall.Statfs_t
	// Linux ST_RDONLY is bit 0. Check each effective file mount independently,
	// before even a cached proof can be reused.
	if err := syscall.Statfs(path, &fs); err != nil || fs.Flags&1 == 0 {
		return "", errors.New("OpenCode scoped native payload requires read-only image mounts")
	}
	return nativePayloadChangeTime(stat)
}
