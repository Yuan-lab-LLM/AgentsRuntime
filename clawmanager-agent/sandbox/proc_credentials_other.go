//go:build !linux

package sandbox

import "fmt"

func readProcessDescriptors(root string, pid int, uid uint32, scoped bool) ([]string, error) {
	if scoped {
		return nil, fmt.Errorf("business proc credential scope requires Linux")
	}
	return processDescriptorLinks(root, pid)
}
