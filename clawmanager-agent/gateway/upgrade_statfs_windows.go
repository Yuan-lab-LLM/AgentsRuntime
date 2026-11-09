//go:build windows

package gateway

func availableFilesystemBytes(string) (uint64, error) {
	return 0, nil
}
