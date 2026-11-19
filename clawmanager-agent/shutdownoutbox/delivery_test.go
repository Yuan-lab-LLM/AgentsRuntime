package shutdownoutbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryFairnessAndForeignScopeCopies(t *testing.T) {
	root := t.TempDir()
	q := New(root, "secret", "scope")
	for i := 0; i < 33; i++ {
		e := event()
		e.ClaimID = fmt.Sprintf("%064x", i)
		if q.Put(e) != nil {
			t.Fatal("put")
		}
	}
	first, _ := q.Pending()
	next, _ := q.Pending()
	seen := map[string]bool{}
	for _, d := range first {
		seen[d.ID] = true
	}
	for _, d := range next {
		seen[d.ID] = true
	}
	if len(seen) != 33 {
		t.Fatal("unacknowledged head starved later records")
	}
	other := New(root, "secret", "foreign-scope")
	if other.prepare(true) != nil {
		t.Fatal("prepare")
	}
	d := first[0]
	raw, _ := json.Marshal(envelope{Delivery: d, MAC: q.mac(d)})
	if os.WriteFile(filepath.Join(other.root, d.ID+".json"), raw, 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := other.Pending(); err == nil {
		t.Fatal("copied foreign scope adopted")
	}
}

func TestDeliveryCanonicalEncoding(t *testing.T) {
	for _, variant := range []string{"newline", "duplicate_generation", "duplicate_mac", "reordered"} {
		t.Run(variant, func(t *testing.T) {
			q := New(t.TempDir(), "secret", "scope")
			e := event()
			if q.Put(e) != nil {
				t.Fatal("put")
			}
			d := e.Delivery()
			file := filepath.Join(q.root, d.ID+".json")
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			switch variant {
			case "newline":
				text += "\n"
			case "duplicate_generation":
				text = strings.Replace(text, `"generation":3`, `"generation":9,"generation":3`, 1)
			case "duplicate_mac":
				text = strings.Replace(text, `"mac":`, `"mac":"bad","mac":`, 1)
			case "reordered":
				text = strings.Replace(text, `"instance_id":8,"owner_user_id":7`, `"owner_user_id":7,"instance_id":8`, 1)
			}
			if os.WriteFile(file, []byte(text), 0600) != nil {
				t.Fatal("fixture")
			}
			if _, err := q.Pending(); err == nil {
				t.Fatal("noncanonical encoding accepted")
			}
			if q.Put(e) == nil {
				t.Fatal("noncanonical evidence overwritten")
			}
			if q.Acknowledge([]Delivery{d}, Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{d.ID}}) == nil {
				t.Fatal("noncanonical evidence deleted")
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != text {
				t.Fatal("evidence changed")
			}
		})
	}
}

func TestDeliveryInspectionWireMAC(t *testing.T) {
	q := New(t.TempDir(), "secret", "scope")
	if q.scope != "2175f6c735708bd66a937e7f9793de2404e840aa8cfec09b807504fa0d8aac5b" || q.mac(event().Delivery()) != "b1e49507a05a405c39654c732ce941395ff16269c11a3fd3044e856e232aee64" {
		t.Fatal("operator inspection wire format drifted")
	}
}

func TestOutboxOperatorMaintenanceBlocksMutationAndMissingNamespace(t *testing.T) {
	for _, kind := range []string{"directory", "partial_file"} {
		t.Run(kind, func(t *testing.T) {
			q := New(t.TempDir(), "secret", "scope")
			e := event()
			if q.Put(e) != nil {
				t.Fatal("put")
			}
			sent, err := q.Pending()
			if err != nil || len(sent) != 1 {
				t.Fatal("pending")
			}
			marker := filepath.Join(filepath.Dir(q.root), ".maintenance-"+q.scope)
			if kind == "directory" {
				err = os.Mkdir(marker, 0700)
			} else {
				err = os.WriteFile(marker, []byte("partial"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.Pending(); err == nil {
				t.Fatal("maintenance read accepted")
			}
			if q.Put(e) == nil {
				t.Fatal("maintenance write accepted")
			}
			if q.Acknowledge(sent, Ack{Version: 1, DiagnosticOnly: true, AcceptedIDs: []string{sent[0].ID}}) == nil {
				t.Fatal("maintenance deletion accepted")
			}
			archived := filepath.Join(q.base, "operator-archived-queue")
			if os.Rename(q.root, archived) != nil {
				t.Fatal("fixture move")
			}
			if _, err := q.Pending(); err == nil {
				t.Fatal("missing maintenance queue treated as empty")
			}
			if q.Put(e) == nil {
				t.Fatal("namespace recreated under maintenance")
			}
			if _, err := os.Lstat(q.root); !os.IsNotExist(err) {
				t.Fatal("queue recreated")
			}
			if os.Remove(marker) != nil {
				t.Fatal("operator marker removal")
			}
			if q.Put(e) != nil {
				t.Fatal("new queue after operator completion")
			}
			if _, err := os.Stat(filepath.Join(archived, sent[0].ID+".json")); err != nil {
				t.Fatal("archived evidence lost")
			}
		})
	}
}
