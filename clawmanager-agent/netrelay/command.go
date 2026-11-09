package netrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
)

const Command = "network-relay"

// RunCommand is an internal agent subcommand. The parent supplies only public
// wiring data on stdin and already-open Unix listener descriptors starting at 3.
func RunCommand(ctx context.Context, args []string, input io.Reader, ready io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != Command {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("netrelay: invalid command")
	}
	if err := helperIdentity(); err != nil {
		return true, err
	}
	b, err := io.ReadAll(io.LimitReader(input, 32769))
	if err != nil || len(b) > 32768 {
		return true, errors.New("netrelay: invalid configuration")
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF {
		return true, errors.New("netrelay: invalid configuration")
	}
	if err = cfg.validate(); err != nil {
		return true, err
	}
	var ingress []net.Listener
	defer func() {
		for _, l := range ingress {
			l.Close()
		}
	}()
	for index := range cfg.IngressPorts {
		f := os.NewFile(uintptr(3+index), "private-ingress")
		if f == nil {
			return true, errors.New("netrelay: inherited listener unavailable")
		}
		l, e := net.FileListener(f)
		f.Close()
		if e != nil {
			return true, errors.New("netrelay: inherited listener unavailable")
		}
		ingress = append(ingress, l)
		if l.Addr().Network() != "unix" {
			return true, errors.New("netrelay: invalid inherited listener")
		}
	}
	l, err := net.Listen("tcp", ":15000")
	if err != nil {
		return true, errors.New("netrelay: transparent listener unavailable")
	}
	defer l.Close()
	if _, err = io.WriteString(ready, "{\"ready\":true}\n"); err != nil {
		return true, err
	}
	return true, Run(ctx, cfg, l, ingress)
}
