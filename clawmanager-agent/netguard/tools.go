package netguard

import (
	"fmt"
	"os"
)

// ValidateRootlessTools checks fixed image dependencies before a worker can
// claim an assignment. It starts no processes and creates no writer state.
func ValidateRootlessTools() error {
	return validateRequiredTools(ipTool, nftTool)
}

func validateRequiredTools(paths ...string) error {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("netguard: required runtime tool %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("netguard: required runtime tool %s is not executable", path)
		}
	}
	return nil
}
