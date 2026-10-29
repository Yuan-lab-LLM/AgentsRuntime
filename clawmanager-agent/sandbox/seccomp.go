package sandbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
)

// Bubblewrap installs this policy after namespace/mount setup and before exec.
// It is inherited by all user descendants. Audit architecture is checked before
// syscall numbers: https://docs.kernel.org/userspace-api/seccomp_filter.html .
// Syscall numbers follow Linux's x86-64/asm-generic UAPI tables. Normal tools,
// sockets, file operations, subprocesses and threads remain supported.
func seccompProgram(arch string) ([]byte, error) {
	var audit, clone uint32
	var denied []uint32
	switch arch {
	case "amd64":
		audit, clone = 0xc000003e, 56
		denied = []uint32{101, 103, 155, 165, 166, 167, 168, 169, 175, 176, 246, 248, 249, 250, 272, 298, 303, 304, 308, 310, 311, 312, 313, 320, 321, 323}
	case "arm64":
		audit, clone = 0xc00000b7, 220
		denied = []uint32{39, 40, 41, 97, 104, 105, 106, 116, 117, 142, 217, 218, 219, 224, 225, 241, 264, 265, 268, 270, 271, 272, 273, 280, 282, 294}
	default:
		return nil, fmt.Errorf("sandbox seccomp does not support %s", arch)
	}
	// New Linux mount API, io_uring and pidfd_getfd are shared across both ABIs.
	denied = append(denied, 425, 428, 429, 430, 431, 432, 433, 438, 442)
	var program []byte
	ins := func(code uint16, yes, no byte, value uint32) {
		var encoded [8]byte
		binary.LittleEndian.PutUint16(encoded[:], code)
		encoded[2], encoded[3] = yes, no
		binary.LittleEndian.PutUint32(encoded[4:], value)
		program = append(program, encoded[:]...)
	}
	const kill, allow, eperm, enosys = 0x80000000, 0x7fff0000, 0x00050001, 0x00050026
	ins(0x20, 0, 0, 4) // seccomp_data.arch
	ins(0x15, 1, 0, audit)
	ins(0x06, 0, 0, kill)
	ins(0x20, 0, 0, 0) // seccomp_data.nr
	if arch == "amd64" {
		ins(0x35, 0, 1, 0x40000000) // reject x32/negative syscall numbers
		ins(0x06, 0, 0, kill)
	}
	for _, number := range denied {
		ins(0x15, 0, 1, number)
		ins(0x06, 0, 0, eperm)
	}
	// clone3 flags live behind a pointer which classic BPF cannot safely inspect.
	// ENOSYS lets libc fall back to clone for ordinary threads and subprocesses.
	ins(0x15, 0, 1, 435)
	ins(0x06, 0, 0, enosys)
	ins(0x15, 0, 3, clone)
	ins(0x20, 0, 0, 16)         // low 32 bits of clone's first argument
	ins(0x45, 0, 1, 0x7e020000) // CLONE_NEW{CGROUP,UTS,IPC,USER,PID,NET,NS}
	ins(0x06, 0, 0, eperm)
	ins(0x06, 0, 0, allow)
	return program, nil
}

// The pipe contains a small immutable program, is consumed once by bubblewrap,
// and is not mounted in a gateway-visible tree.
func seccompFilter() (*os.File, error) {
	program, err := seccompProgram(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_, err = writer.Write(program)
	closeErr := writer.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		reader.Close()
		return nil, err
	}
	return reader, nil
}
