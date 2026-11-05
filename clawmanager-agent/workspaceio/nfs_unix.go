//go:build linux || darwin

package workspaceio

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/sandboxprofile"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const NFSManagementUID = sandboxprofile.ManagementUID
const NFSManagementGID = sandboxprofile.ManagementGID

// PrepareNFSManagementRoots never takes ownership of an existing tree. The NFS
// administrator must provision its root for the reserved management identity.
func PrepareNFSManagementRoots(base string, children ...string) error {
	if err := validateNFSRootPath(base); err != nil {
		return err
	}
	root, err := Open(base)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkNFSManagement(root); err != nil {
		return fmt.Errorf("NFS workspace root must be provisioned for management UID 10001: %w", err)
	}
	for _, name := range children {
		child, err := root.Child(name, 0711)
		if err != nil {
			return err
		}
		err = checkNFSManagement(child)
		child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func checkNFSManagement(root *FS) error {
	info, err := root.Root.Stat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != NFSManagementUID || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("management directory requires owner 10001 and no group/other write permission")
	}
	// Only fixed management directories receive traversal permission. User data
	// never passes through this operation.
	if info.Mode().Perm()&0111 != 0111 {
		return root.Chmod(".", info.Mode().Perm()|0111)
	}
	return nil
}

func PrepareNFSInstance(base, runtimeType string, userID, instanceID, uid, gid int) error {
	if runtimeType == "" || runtimeType == "." || runtimeType == ".." || strings.ContainsAny(runtimeType, "/\\") || userID < 1 || instanceID < 1 || uid < 200001 || uint64(uid) >= 1<<32-1 || gid != uid {
		return fmt.Errorf("invalid NFS instance identity")
	}
	if err := validateNFSRootPath(base); err != nil {
		return err
	}
	root, err := Open(base)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkNFSManagement(root); err != nil {
		return err
	}
	current := root
	defer func() {
		if current != root {
			current.Close()
		}
	}()
	for _, part := range []string{runtimeType, "user-" + strconv.Itoa(userID)} {
		next, err := current.Child(part, 0711)
		if err != nil {
			return err
		}
		if current != root {
			current.Close()
		}
		current = next
		if err = checkNFSManagement(current); err != nil {
			return err
		}
	}
	if err = CheckNFSSource(base, filepath.Join(current.Base, "instance-"+strconv.Itoa(instanceID))); err != nil {
		return err
	}
	return ensureNFSGrant(current, "instance-"+strconv.Itoa(instanceID), uid, gid, false)
}

// PrepareNFSTmp returns only the exact persistent instance tmp leaf.
func PrepareNFSTmp(base, runtimeType string, userID, instanceID, uid, gid int) (string, error) {
	if err := PrepareNFSInstance(base, runtimeType, userID, instanceID, uid, gid); err != nil {
		return "", err
	}
	current, err := Open(base)
	if err != nil {
		return "", err
	}
	defer func() { current.Close() }()
	for _, part := range []string{".clawmanager-instance-tmp", runtimeType, "user-" + strconv.Itoa(userID), "instance-" + strconv.Itoa(instanceID)} {
		next, err := current.Child(part, 0711)
		if err != nil {
			return "", err
		}
		current.Close()
		current = next
		if err = checkNFSManagement(current); err != nil {
			return "", err
		}
	}
	if err = CheckNFSSource(base, filepath.Join(current.Base, "tmp")); err != nil {
		return "", err
	}
	if err = ensureNFSGrant(current, "tmp", uid, gid, true); err != nil {
		return "", err
	}
	return filepath.Join(current.Base, "tmp"), nil
}

func ensureNFSGrant(parent *FS, name string, uid, gid int, tmp bool) error {
	mode := os.FileMode(0770)
	if tmp {
		mode |= os.ModeSticky
	}
	err := parent.Mkdir(name, mode.Perm())
	created := err == nil
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("instance grant must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("instance ownership unavailable")
	}
	if stat.Uid == uint32(uid) { // Preserve legacy data, including its historical Team group.
		if tmp && info.Mode()&(os.ModePerm|os.ModeSticky) != os.ModeSticky|0777 {
			return fmt.Errorf("legacy tmp permissions mismatch")
		}
		if !tmp && info.Mode().Perm()&0002 != 0 {
			return fmt.Errorf("legacy workspace is writable by others")
		}
		return nil
	}
	if stat.Uid != NFSManagementUID || stat.Gid != uint32(gid) {
		return fmt.Errorf("instance grant ownership mismatch; prepare using management UID and instance primary GID")
	}
	if created {
		if err = parent.Chmod(name, mode); err != nil {
			return err
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return err
		}
	}
	if info.Mode()&(os.ModePerm|os.ModeSticky|os.ModeSetgid|os.ModeSetuid) != mode {
		return fmt.Errorf("instance grant permissions mismatch")
	}
	return nil
}

func PrepareNFSTeam(base string, userID int, team string, gid int) error {
	if userID < 1 || team == "" || team == "." || team == ".." || strings.ContainsAny(team, "/\\") || gid <= 0 {
		return fmt.Errorf("invalid Team grant identity")
	}
	root, err := Open(base)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkNFSManagement(root); err != nil {
		return err
	}
	current := root
	defer func() {
		if current != root {
			current.Close()
		}
	}()
	for _, part := range []string{"teams", "user-" + strconv.Itoa(userID)} {
		next, err := current.Child(part, 0711)
		if err != nil {
			return err
		}
		if current != root {
			current.Close()
		}
		current = next
		if err = checkNFSManagement(current); err != nil {
			return err
		}
	}
	name := "team-" + team + "-shared"
	if err = CheckNFSSource(base, filepath.Join(current.Base, name)); err != nil {
		return err
	}
	err = current.Mkdir(name, 0770)
	created := err == nil
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, err := current.Lstat(name)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Gid != uint32(gid) || info.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("Team grant ownership or permissions mismatch")
	}
	if created {
		return current.Chmod(name, os.ModeSetgid|0770)
	}
	if info.Mode()&os.ModeSetgid == 0 || info.Mode().Perm()&0070 != 0070 {
		return fmt.Errorf("existing Team root requires an administrator-provisioned shared setgid grant")
	}
	return nil
}

func validateNFSRootPath(base string) error {
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return fmt.Errorf("NFS root must be canonical and absolute")
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return err
	}
	if resolved != base {
		return fmt.Errorf("NFS root contains a symlink")
	}
	for path := base; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != 10001) || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("NFS source ancestor is not management-controlled")
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}

// CheckNFSSource rejects mounts below the configured storage mount, including
// same-device bind mounts. Device-number equality alone cannot detect those.
func CheckNFSSource(base, target string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	within := func(parent, child string) bool {
		return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := unescape.Replace(fields[4])
		if mount != base && within(base, mount) && (within(mount, target) || within(target, mount)) {
			return fmt.Errorf("NFS source contains an unexpected nested mount")
		}
	}
	return nil
}

// PrepareNFSCapsule grants only one management operation's data leaf. Lease,
// operation-lock and parent bookkeeping directories are never granted.
func PrepareNFSCapsule(base, rollout string, instanceID, gid int) error {
	if rollout == "" || rollout == "." || rollout == ".." || strings.ContainsAny(rollout, "/\\") || instanceID < 1 || gid < 200001 {
		return fmt.Errorf("invalid capsule identity")
	}
	if err := validateNFSRootPath(base); err != nil {
		return err
	}
	current, err := Open(base)
	if err != nil {
		return err
	}
	defer func() { current.Close() }()
	for _, part := range []string{".clawmanager-upgrades", "rollout-" + rollout} {
		next, err := current.Child(part, 0711)
		if err != nil {
			return err
		}
		current.Close()
		current = next
		if err = checkNFSManagement(current); err != nil {
			return err
		}
	}
	name := fmt.Sprintf("instance-%d", instanceID)
	if err = CheckNFSSource(base, filepath.Join(current.Base, name)); err != nil {
		return err
	}
	if err = current.Mkdir(name, 0770); err != nil && !os.IsExist(err) {
		return err
	}
	leaf, err := current.Child(name, 0770)
	if err != nil {
		return err
	}
	defer leaf.Close()
	info, err := leaf.Stat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != NFSManagementUID || info.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("capsule requires existing management ownership")
	}
	file, err := leaf.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	if stat.Gid != uint32(gid) {
		if err = file.Chown(-1, gid); err != nil {
			return err
		}
	}
	return file.Chmod(0770)
}
