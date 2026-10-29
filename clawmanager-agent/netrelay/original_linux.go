//go:build linux

package netrelay

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"syscall"
	"unsafe"
)

func originalDestination(c net.Conn) (string, error) {
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return "", errors.New("netrelay: TCP required")
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return "", err
	}
	var result string
	err = raw.Control(func(fd uintptr) {
		// Linux SO_ORIGINAL_DST/IP6T_SO_ORIGINAL_DST both use option 80.
		for _, level := range []uintptr{syscall.IPPROTO_IP, syscall.IPPROTO_IPV6} {
			var address [128]byte
			n := uint32(len(address))
			_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, level, 80, uintptr(unsafe.Pointer(&address[0])), uintptr(unsafe.Pointer(&n)), 0)
			if errno != 0 {
				continue
			}
			family := binary.NativeEndian.Uint16(address[:2])
			port := binary.BigEndian.Uint16(address[2:4])
			if family == syscall.AF_INET && n >= 16 {
				result = net.JoinHostPort(net.IP(address[4:8]).String(), strconv.Itoa(int(port)))
				return
			}
			if family == syscall.AF_INET6 && n >= 28 {
				result = net.JoinHostPort(net.IP(address[8:24]).String(), strconv.Itoa(int(port)))
				return
			}
		}
	})
	if err != nil {
		return "", err
	}
	if !validAddress(result) {
		return "", errors.New("netrelay: original destination unavailable")
	}
	return result, nil
}
