//go:build !linux

package receiptstorage

import "fmt"

func Ensure(root string, id Identity, create bool, excluded ...string) error {
	if err := Validate(root, id, excluded...); err != nil {
		return err
	}
	return fmt.Errorf("Pro receipt ownership isolation requires Linux")
}
