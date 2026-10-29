package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestSharedWriterRecordRejectsStaleOrUnconfirmedOwnership(t *testing.T) {
	path := t.TempDir() + "/writer.json"
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 5, AgentType: "openclaw", PodUID: "source"}
	if err := claimWriter(path, req, "slot-0"); err != nil {
		t.Fatal(err)
	}
	newer := req
	newer.Generation++
	newer.PodUID = "target"
	if err := claimWriter(path, newer, "slot-0"); err == nil {
		t.Fatal("unconfirmed source handover accepted")
	}
	if err := stopWriter(path, req, "slot-0"); err != nil {
		t.Fatal(err)
	}
	if err := claimWriter(path, newer, "slot-1"); err != nil {
		t.Fatal(err)
	}
	if err := stopWriter(path, newer, "slot-1"); err != nil {
		t.Fatal(err)
	}
	if err := claimWriter(path, req, "slot-0"); err == nil {
		t.Fatal("stale generation resurrected after target stopped")
	}
	newer.PodUID = "reused-ip"
	if err := claimWriter(path, newer, "slot-1"); err == nil {
		t.Fatal("same generation changed Pod UID")
	}
}
