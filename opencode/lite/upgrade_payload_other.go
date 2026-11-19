//go:build !unix

package opencode

import (
	"errors"
	"os"
)

func trustedNativePayloadChange(os.FileInfo) (string, error) {
	return "", errors.New("OpenCode native preservation requires a Unix image")
}
