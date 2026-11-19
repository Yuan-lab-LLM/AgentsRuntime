//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

func namespaceOwner(_, _ *os.File, _ bool) (*os.File, error) {
	return nil, fmt.Errorf("terminal namespace attachment requires Linux")
}
