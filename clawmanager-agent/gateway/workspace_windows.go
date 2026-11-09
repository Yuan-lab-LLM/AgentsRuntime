//go:build windows

package gateway

import (
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

func ChownWorkspace(string, int, int) error {
	return nil
}

func ChgrpWorkspace(string, int) error {
	return nil
}

func teamDirectoryGroup(root *workspaceio.FS) (int, error) {
	return 0, fmt.Errorf("Team group requires Unix")
}
