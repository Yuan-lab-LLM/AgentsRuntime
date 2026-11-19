package process

import (
	"context"
	"errors"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/shutdownoutbox"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestShutdownDeliveryPersistenceFailureCannotBeBypassedByRepeatedStop(t *testing.T) {
	env, proof := shutdownNodeFixture(t)
	ownerFile := filepath.Join(proof.directory, ".task-writer.lock", "owner.json")
	if os.Remove(ownerFile) != nil || os.Remove(filepath.Dir(ownerFile)) != nil {
		t.Fatal("fixture")
	}
	m := New(appconfig.Config{})
	blocked := true
	var owners []int
	m.ConfigureShutdownObservationSink(func() int { return 9 }, func(owner int, value ShutdownObservation) error {
		owners = append(owners, owner)
		if blocked {
			return errors.New("disk unavailable")
		}
		return nil
	})
	m.shutdownOwnerAtStart = 7
	if m.auditStoppedProcess(&exec.Cmd{Env: env}, proof, nil, "stopped") == nil {
		t.Fatal("failed persistence reported successful")
	}
	if m.shutdownAudit != "cleanup_confirmed_release_observed" || m.shutdownPending == nil {
		t.Fatal("lost native result or pending observation")
	}
	if m.Stop(context.Background()) == nil || m.Restart(context.Background()) == nil {
		t.Fatal("pending persistence bypassed")
	}
	blocked = false
	if m.Stop(context.Background()) != nil || m.shutdownPending != nil {
		t.Fatal("retry failed")
	}
	for _, owner := range owners {
		if owner != 7 {
			t.Fatal("observation rebound to current session owner")
		}
	}
}
func TestShutdownDeliveryOutboxRestoresAfterManagerRecreation(t *testing.T) {
	env, proof := shutdownNodeFixture(t)
	ownerFile := filepath.Join(proof.directory, ".task-writer.lock", "owner.json")
	if os.Remove(ownerFile) != nil || os.Remove(filepath.Dir(ownerFile)) != nil {
		t.Fatal("fixture")
	}
	root := t.TempDir()
	q := shutdownoutbox.New(root, "test-key", "test-scope")
	m := New(appconfig.Config{})
	m.ConfigureShutdownObservationSink(func() int { return 7 }, func(owner int, value ShutdownObservation) error {
		return q.Put(shutdownoutbox.Event{InstanceID: 8, OwnerUserID: owner, Generation: value.Generation, ClaimID: value.ClaimID, Outcome: value.Outcome})
	})
	m.shutdownOwnerAtStart = 7
	if m.auditStoppedProcess(&exec.Cmd{Env: env}, proof, nil, "stopped") != nil {
		t.Fatal("enqueue failed")
	}
	restored := shutdownoutbox.New(root, "test-key", "test-scope")
	items, err := restored.Pending()
	if err != nil || len(items) != 1 || items[0].Event.OwnerUserID != 7 || items[0].Event.ClaimID != proof.claim {
		t.Fatalf("%v %v", items, err)
	}
}
