package sandbox

import (
	"encoding/binary"
	"testing"
)

// Evaluate the serialized policy over synthetic seccomp_data. This exercises
// branch offsets and architecture checks without depending on the host kernel.
func filterAction(t *testing.T, filter []byte, arch, call, flags uint32) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data, call)
	binary.LittleEndian.PutUint32(data[4:], arch)
	binary.LittleEndian.PutUint32(data[16:], flags)
	var value uint32
	for pc, steps := 0, 0; pc < len(filter)/8 && steps < 512; pc, steps = pc+1, steps+1 {
		ins := filter[pc*8:]
		code, k := binary.LittleEndian.Uint16(ins), binary.LittleEndian.Uint32(ins[4:])
		switch code {
		case 0x20:
			value = binary.LittleEndian.Uint32(data[k:])
		case 0x15:
			if value == k {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x35:
			if value >= k {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x45:
			if value&k != 0 {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x06:
			return k
		default:
			t.Fatalf("unexpected BPF opcode %#x", code)
		}
	}
	t.Fatal("seccomp policy has no reachable verdict")
	return 0
}

func TestSeccompPolicyPreservesNormalToolsAndRejectsPrivilegedOperations(t *testing.T) {
	for _, tc := range []struct {
		arch                             string
		audit, read, mount, trace, clone uint32
	}{{"amd64", 0xc000003e, 0, 165, 101, 56}, {"arm64", 0xc00000b7, 63, 40, 117, 220}} {
		t.Run(tc.arch, func(t *testing.T) {
			filter, err := seccompProgram(tc.arch)
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range []uint32{tc.read, tc.clone} {
				if got := filterAction(t, filter, tc.audit, call, 0x00010f00); got != 0x7fff0000 {
					t.Fatalf("ordinary tool/thread syscall %d denied: %#x", call, got)
				}
			}
			for _, call := range []uint32{tc.mount, tc.trace, 428, 429, 430, 431, 432, 433, 442} {
				if got := filterAction(t, filter, tc.audit, call, 0); got != 0x00050001 {
					t.Fatalf("privileged syscall %d allowed: %#x", call, got)
				}
			}
			if got := filterAction(t, filter, tc.audit, tc.clone, 0x10000000); got != 0x00050001 {
				t.Fatal("nested namespace clone allowed")
			}
			if got := filterAction(t, filter, tc.audit, 435, 0); got != 0x00050026 {
				t.Fatal("clone3 must return ENOSYS for libc thread fallback")
			}
			if got := filterAction(t, filter, 0x40000003, tc.read, 0); got != 0x80000000 {
				t.Fatal("foreign syscall ABI accepted")
			}
			if tc.arch == "amd64" && filterAction(t, filter, tc.audit, 0x40000000, 0) != 0x80000000 {
				t.Fatal("x32 syscall ABI accepted")
			}
		})
	}
	if _, err := seccompProgram("386"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}
