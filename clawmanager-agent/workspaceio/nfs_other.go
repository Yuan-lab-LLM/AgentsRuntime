//go:build !linux && !darwin

package workspaceio

import "fmt"

func PrepareNFSManagementRoots(base string, children ...string) error {
	return fmt.Errorf("NFS rootless storage requires Linux")
}
func PrepareNFSInstance(base, runtimeType string, userID, instanceID, uid, gid int) error {
	return fmt.Errorf("NFS rootless storage requires Linux")
}
func PrepareNFSTmp(base, runtimeType string, userID, instanceID, uid, gid int) (string, error) {
	return "", fmt.Errorf("NFS rootless storage requires Linux")
}
func PrepareNFSTeam(base string, userID int, team string, gid int) error {
	return fmt.Errorf("NFS rootless storage requires Linux")
}
func CheckNFSSource(base, target string) error {
	return fmt.Errorf("NFS rootless storage requires Linux")
}
func PrepareNFSCapsule(base, rollout string, instanceID, gid int) error {
	return fmt.Errorf("NFS rootless storage requires Linux")
}
