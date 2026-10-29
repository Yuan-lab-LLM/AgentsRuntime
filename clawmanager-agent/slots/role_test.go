package slots

import (
	"context"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestPoolRejectsInvalidCountBeforeAllocatingClients(t *testing.T) {
	for _, raw := range []string{"-1", "0", "257", "bad"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("RUNTIME_SLOT_COUNT", raw)
			if err := runPool(context.Background(), gateway.Config{}); err == nil {
				t.Fatal("invalid slot count accepted")
			}
		})
	}
}
