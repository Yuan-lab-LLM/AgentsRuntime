//go:build !linux && !darwin

package slots

import (
	"fmt"
	"os"
)

func prepareTmpDirectory(*os.File, bool, bool, int, int) error {
	return fmt.Errorf("persistent tmp requires Unix directory ownership")
}
