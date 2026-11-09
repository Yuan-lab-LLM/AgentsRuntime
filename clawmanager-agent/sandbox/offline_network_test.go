package sandbox

import "testing"

func TestOfflineMaintenanceGetsFreshNetwork(t *testing.T) {
	spec := testSpec(t)
	spec.OfflineNetwork = true
	args, err := buildCommand(spec, "/usr/bin/bwrap", nil)
	if err != nil || !containsArgs(args, []string{"--unshare-net"}) {
		t.Fatalf("offline command inherited caller network: %v %v", args, err)
	}
	spec.NetworkHostsFile = "/run/clawmanager-network-public/other/hosts"
	spec.NetworkResolvConf = "/run/clawmanager-network-public/other/resolv.conf"
	if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
		t.Fatal("offline maintenance accepted business broker routing")
	}
}
