//go:build !linux

package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
)

func protectNativeStorageHelper() error { return fmt.Errorf("native storage helper requires Linux") }

func openNativeSkillInput(*workspaceio.FS, string, bool) (*os.File, error) {
	return nil, fmt.Errorf("native skill scan requires Linux")
}
