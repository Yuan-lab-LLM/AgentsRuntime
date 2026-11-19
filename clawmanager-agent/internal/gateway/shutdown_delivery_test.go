package gateway

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/internal/shutdownoutbox"
	"os"
	"path/filepath"
	"testing"
)

func shutdownDeliveryStoppedFixture(t *testing.T) (*GatewayManager, ConfirmStopRequest, *shutdownProof) {
	var release func() error
	m, req, proof := liteShutdownFixture(t, func(context.Context) error { return release() })
	ownerFile := filepath.Join(proof.directory, ".task-writer.lock", "owner.json")
	// The manager captures the original claim BEFORE native Stop releases it.
	release = func() error {
		for _, target := range []string{ownerFile, filepath.Dir(ownerFile)} {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	return m, req, proof
}
func TestShutdownDeliverySurvivesSuccessfulGatewayDeletion(t *testing.T) {
	m, req, proof := shutdownDeliveryStoppedFixture(t)
	m.gateways[req.GatewayID].state.UserID = 7
	root := t.TempDir()
	q := shutdownoutbox.New(root, "test-key", "test-scope")
	m.ConfigureShutdownObservationSink(func(state GatewayState) error {
		o := state.ShutdownObservation
		return q.Put(shutdownoutbox.Event{InstanceID: state.InstanceID, OwnerUserID: state.UserID, Generation: o.Generation, ClaimID: o.ClaimID, Outcome: o.Outcome})
	})
	if err := m.StopGatewayConfirmed(context.Background(), req.GatewayID); err != nil {
		t.Fatal(err)
	}
	if len(m.GatewayStates()) != 0 {
		t.Fatal("gateway not deleted")
	}
	restored := shutdownoutbox.New(root, "test-key", "test-scope")
	items, err := restored.Pending()
	if err != nil || len(items) != 1 || items[0].Event.ClaimID != proof.claim || items[0].Event.Outcome != "cleanup_confirmed_release_observed" {
		t.Fatalf("%v %v", items, err)
	}
}
func TestShutdownDeliveryFailureRetainsGatewayAndPortUntilRetry(t *testing.T) {
	m, req, _ := shutdownDeliveryStoppedFixture(t)
	blocked := true
	m.ConfigureShutdownObservationSink(func(GatewayState) error {
		if blocked {
			return errors.New("disk unavailable")
		}
		return nil
	})
	if m.StopGatewayConfirmed(context.Background(), req.GatewayID) == nil {
		t.Fatal("lost final observation")
	}
	state := m.gateways[req.GatewayID]
	if state == nil || state.deleteBusy || !state.deletePending {
		t.Fatal("lost binding or retryability")
	}
	if _, err := m.ports.Reserve(99, 1, PortRange{Start: 31000, End: 31000}); err == nil {
		t.Fatal("released port before persistence")
	}
	blocked = false
	if m.StopGatewayConfirmed(context.Background(), req.GatewayID) != nil || len(m.GatewayStates()) != 0 {
		t.Fatal("retry failed")
	}
}
