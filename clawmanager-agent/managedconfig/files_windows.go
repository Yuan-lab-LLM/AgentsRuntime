//go:build windows

package managedconfig

import (
	"context"
	"os"
)

// The protected native filesystem migration requires Unix file identity and
// no-follow semantics. Unsupported platforms cannot certify migration.
func Rewrite(context.Context, string, string, []Directory) error { return ErrUnsafe }

func Create(context.Context, string, string, string, string, []byte, os.FileMode, int, int) error {
	return ErrUnsafe
}

func Subdirectories(context.Context, string, string, string) ([]string, error) { return nil, ErrUnsafe }
