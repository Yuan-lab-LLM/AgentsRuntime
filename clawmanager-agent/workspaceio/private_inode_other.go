//go:build !linux

package workspaceio

import (
	"fmt"
	"os"
)

func ValidateNFSPrivateTree(string, int, bool) error {
	return fmt.Errorf("NFS inode admission requires Linux")
}

func ValidateNFSPrivateFD(*os.File, int, bool) error {
	return fmt.Errorf("NFS inode admission requires Linux")
}
