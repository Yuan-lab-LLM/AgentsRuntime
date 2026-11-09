//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package netguard

import "os"

func ownedBy(os.FileInfo, uint32) bool { return false }

func fileReceipt(os.FileInfo) any { return nil }
