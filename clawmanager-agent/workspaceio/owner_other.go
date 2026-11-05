//go:build !unix

package workspaceio

import (
	"fmt"
	"os"
)

func ownedBySupervisor(os.FileInfo) bool { return false }

func ValidateInstanceCaller(uid, gid int) error {
	return fmt.Errorf("instance ownership requires Unix")
}

func groupMatches(info os.FileInfo, gid int) bool { return false }
