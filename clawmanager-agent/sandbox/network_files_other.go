//go:build !linux && !darwin

package sandbox

import "os"

func networkFileOwner(os.FileInfo, uint32) bool { return false }
