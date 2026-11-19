//go:build !linux && !darwin

package slots

import "os"

func trustedOwner(os.FileInfo) bool { return false }

func rootOwned(os.FileInfo) bool { return false }
