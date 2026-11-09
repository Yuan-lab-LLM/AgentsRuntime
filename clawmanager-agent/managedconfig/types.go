package managedconfig

import "errors"

const MaxFileBytes = 4 << 20
const MaxFiles = 256
const MaxEntries = 4096
const maxTotalBytes = 16 << 20

var ErrUnsafe = errors.New("managed credential migration requires safe generated files")

type Transform func([]byte) ([]byte, error)
type Directory struct {
	Path   string
	Select func(string) Transform
}
