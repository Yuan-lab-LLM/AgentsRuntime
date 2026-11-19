//go:build !linux

package netrelay

import "errors"

func helperIdentity() error { return errors.New("netrelay: Linux required") }
