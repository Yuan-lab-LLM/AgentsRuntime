//go:build windows

package gateway

func ChownWorkspace(string, int, int) error {
	return nil
}

func ChgrpWorkspace(string, int) error {
	return nil
}
