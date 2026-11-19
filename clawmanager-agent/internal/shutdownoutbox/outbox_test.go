package shutdownoutbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func event() Event {
	return Event{InstanceID: 8, OwnerUserID: 7, Generation: 3, ClaimID: strings.Repeat("a", 64), Outcome: "cleanup_confirmed_release_observed"}
}
func TestOutboxRestartDeduplicationAndExplicitAck(t *testing.T) {
	root := t.TempDir()
	q := New(root, "secret", "endpoint/instance-8")
	e := event()
	if q.Put(e) != nil || q.Put(e) != nil {
		t.Fatal("put")
	}
	restored := New(root, "secret", "endpoint/instance-8")
	sent, err := restored.Pending()
	if err != nil || len(sent) != 1 {
		t.Fatalf("%v %v", sent, err)
	}
	for _, a := range []Ack{{}, {Version: 1, DiagnosticOnly: true}, {Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{strings.Repeat("b", 64)}}, {Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{sent[0].ID, sent[0].ID}}} {
		if restored.Acknowledge(sent, a) == nil {
			t.Fatal("invalid ack accepted")
		}
		items, _ := restored.Pending()
		if len(items) != 1 {
			t.Fatal("invalid ack lost data")
		}
	}
	if restored.Acknowledge(sent, Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{}}) != nil {
		t.Fatal("empty ack")
	}
	items, _ := restored.Pending()
	if len(items) != 1 {
		t.Fatal("empty ack deleted data")
	}
	ack := Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{sent[0].ID}}
	if restored.Acknowledge(sent, ack) != nil || restored.Acknowledge(sent, ack) != nil {
		t.Fatal("ack/idempotence")
	}
	items, _ = restored.Pending()
	if len(items) != 0 {
		t.Fatal("ack")
	}
}
func TestOutboxScopeIsolationAndCorruptionRetained(t *testing.T) {
	root := t.TempDir()
	q := New(root, "secret", "scope")
	if q.Put(event()) != nil {
		t.Fatal("put")
	}
	items, _ := q.Pending()
	for _, other := range []*Queue{New(root, "rotated", "scope"), New(root, "secret", "other")} {
		found, err := other.Pending()
		if err != nil || len(found) != 0 {
			t.Fatal("foreign scope adopted")
		}
	}
	file := filepath.Join(q.root, items[0].ID+".json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("secret stored")
	}
	if os.WriteFile(file, []byte(`{"partial":`), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := q.Pending(); err == nil {
		t.Fatal("partial file accepted")
	}
	if q.Put(event()) == nil {
		t.Fatal("partial overwritten")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("evidence removed")
	}
}
func TestOutboxCapacityAndBoundedBatches(t *testing.T) {
	q := New(t.TempDir(), "secret", "scope")
	if q.prepare(true) != nil {
		t.Fatal("prepare")
	}
	for i := 0; i < Limit; i++ {
		e := event()
		e.ClaimID = fmt.Sprintf("%064x", i)
		d := e.Delivery()
		raw, _ := json.Marshal(envelope{Delivery: d, MAC: q.mac(d)})
		if os.WriteFile(filepath.Join(q.root, d.ID+".json"), raw, 0600) != nil {
			t.Fatal("fixture")
		}
	}
	items, err := q.Pending()
	if err != nil || len(items) != 32 {
		t.Fatalf("%d %v", len(items), err)
	}
	e := event()
	e.ClaimID = fmt.Sprintf("%064x", Limit)
	if q.Put(e) == nil {
		t.Fatal("capacity exceeded")
	}
	if q.Acknowledge(items, Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{items[0].ID}}) != nil {
		t.Fatal("ack")
	}
	if q.Put(e) != nil {
		t.Fatal("space not reclaimed")
	}
}
func TestOutboxUnsafePathsAndTampering(t *testing.T) {
	if New("relative", "key", "scope") != nil || New(t.TempDir(), "", "scope") != nil {
		t.Fatal("unsafe options")
	}
	q := New(t.TempDir(), "secret", "scope")
	if q.Put(event()) != nil {
		t.Fatal("put")
	}
	items, _ := q.Pending()
	file := filepath.Join(q.root, items[0].ID+".json")
	raw, _ := os.ReadFile(file)
	raw = []byte(strings.Replace(string(raw), "cleanup_confirmed_release_observed", "cleanup_unconfirmed", 1))
	_ = os.WriteFile(file, raw, 0600)
	if _, err := q.Pending(); err == nil {
		t.Fatal("tampered payload accepted")
	}
}
func TestOutboxUnknownEntriesBlockWithoutDeletion(t *testing.T) {
	q := New(t.TempDir(), "secret", "scope")
	if q.Put(event()) != nil {
		t.Fatal("put")
	}
	file := filepath.Join(q.root, "unknown")
	if os.WriteFile(file, []byte("unknown"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := q.Pending(); err == nil {
		t.Fatal("unknown file ignored")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("unknown file deleted")
	}
}
