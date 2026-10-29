//go:build !linux

package hermes

import "os"

// The managed Lite image and its isolation contract are Linux-only.
func desktopReleaseOwnerTrusted(os.FileInfo) bool { return false }
