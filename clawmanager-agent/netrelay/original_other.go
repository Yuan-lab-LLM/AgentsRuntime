//go:build !linux

package netrelay

import (
	"errors"
	"net"
)

func originalDestination(net.Conn) (string, error) { return "", errors.New("netrelay: Linux required") }
