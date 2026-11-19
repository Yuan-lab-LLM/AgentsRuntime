package supervisor

import (
	"encoding/json"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/protocol"
	"strings"
	"testing"
)

func TestShutdownObservationPayloadIsBoundedAndLegacyOptional(t *testing.T) {
	legacy, err := json.Marshal(protocol.RuntimePayload{ShutdownObservation: shutdownObservationPayload(nil)})
	if err != nil || strings.Contains(string(legacy), "shutdown_observation") {
		t.Fatalf("legacy payload changed: %s %v", legacy, err)
	}
	value := &process.ShutdownObservation{Generation: 7, ClaimID: strings.Repeat("a", 64), Outcome: "cleanup_unconfirmed"}
	wire := shutdownObservationPayload(value)
	wire.ClaimID = "edited"
	if value.ClaimID == wire.ClaimID {
		t.Fatal("observation pointer escaped")
	}
	raw, err := json.Marshal(shutdownObservationPayload(value))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || fields["generation"] != float64(7) {
		t.Fatalf("unsafe observation: %s", raw)
	}
}
