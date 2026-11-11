//go:build !linux

package workbench

import "io"

func prepareSharedTerminal(io.Reader) error { return Invalid() }
