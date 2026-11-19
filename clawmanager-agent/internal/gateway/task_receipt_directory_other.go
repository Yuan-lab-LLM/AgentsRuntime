//go:build !linux

package gateway

import "fmt"

func prepareReceiptDirectory(root, name string, uid, gid int) error {
	return fmt.Errorf("managed receipt storage requires Linux ownership isolation")
}
