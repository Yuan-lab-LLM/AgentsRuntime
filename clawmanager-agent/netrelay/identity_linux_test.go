//go:build linux

package netrelay

import (
	"strings"
	"testing"
)

func TestRestrictedStatusRequiresNoRecoverablePrivileges(t *testing.T) {
	// A capability bounding set is a ceiling, not a grant. With no effective,
	// permitted, inheritable or ambient capabilities and NoNewPrivs, exec cannot
	// regain capabilities even when the Pod supervisor cannot lower that ceiling.
	status := "CapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t00000000002810eb\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\n"
	if !restrictedStatus([]byte(status)) {
		t.Fatal("rejected unprivileged NNP helper")
	}
	for _, bad := range []string{strings.Replace(status, "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1), strings.Replace(status, "CapEff:\t0000000000000000", "CapEff:\t0000000000000001", 1), strings.Replace(status, "CapPrm:\t0000000000000000\n", "", 1)} {
		if restrictedStatus([]byte(bad)) {
			t.Fatal("accepted privileged/incomplete status")
		}
	}
}
