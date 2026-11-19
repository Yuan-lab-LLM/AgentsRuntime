package slots

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestAssignmentRejectsConcurrentWriterAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	b, err := openBook(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 3, AgentType: "openclaw", GatewayPort: 20000}
	if err = b.begin(req); err != nil {
		t.Fatal(err)
	}
	if err = b.begin(req); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	changed := req
	changed.Generation++
	if err = b.begin(changed); err == nil {
		t.Fatal("successor started before confirmed stop")
	}
	changed = req
	changed.UserID++
	if err = b.begin(changed); err == nil {
		t.Fatal("identity mutation accepted")
	}
	restored, err := openBook(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.quarantined {
		t.Fatal("lost writer state was treated as stopped")
	}
	if err = restored.begin(req); err == nil {
		t.Fatal("quarantined writer restarted")
	}
	if err = b.stopped("gw-7-3"); err != nil {
		t.Fatal(err)
	}
	changed = req
	changed.Generation++
	if err = b.begin(changed); err != nil {
		t.Fatalf("confirmed handover: %v", err)
	}
}
