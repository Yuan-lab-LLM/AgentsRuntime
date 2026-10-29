//go:build linux

package netrelay

import (
	"errors"
	"os"
	"strings"
)

func helperIdentity() error {
	fail := func() error { return errors.New("netrelay: helper identity is not restricted") }
	if os.Geteuid() != 65534 || os.Getuid() != 65534 || os.Getgid() != 65534 || os.Getegid() != 65534 {
		return fail()
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		return fail()
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fail()
	}
	if !restrictedStatus(b) {
		return fail()
	}
	return nil
}

func restrictedStatus(b []byte) bool {
	// Bounding sets do not grant capabilities. Without CAP_SETPCAP the outer
	// supervisor cannot lower that ceiling; NNP plus zero permitted/inheritable/
	// ambient sets still prevent exec from obtaining capabilities or setuid gains.
	required := map[string]string{"CapInh:": "0000000000000000", "CapPrm:": "0000000000000000", "CapEff:": "0000000000000000", "CapAmb:": "0000000000000000", "NoNewPrivs:": "1"}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if expected, ok := required[fields[0]]; ok {
			if fields[1] != expected {
				return false
			}
			delete(required, fields[0])
		}
	}
	return len(required) == 0
}
