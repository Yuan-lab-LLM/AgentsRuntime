//go:build !linux

package opencode

import (
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
)

func validateNativePayloadNamespace(int, int, *gateway.NativeUpgradeParentProof, [2]string) error {
	return errors.New("OpenCode scoped payload requires Linux namespaces")
}

func scopedNativePayloadChange(string, os.FileInfo) (string, error) {
	return "", errors.New("OpenCode scoped payload requires Linux namespaces")
}
