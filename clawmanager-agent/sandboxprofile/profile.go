// Package sandboxprofile identifies the explicitly selected Runtime isolation
// contract. An empty profile preserves the existing Pod-userns implementation.
package sandboxprofile

const (
	NFSRootlessV1 = "nfs-rootless-v1"
	ManagementUID = 10001
	ManagementGID = 10001
)

func Valid(profile string) bool { return profile == "" || profile == NFSRootlessV1 }
