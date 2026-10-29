//go:build !unix

package workspaceio

import "os"

func ownedBySupervisor(os.FileInfo) bool { return false }
