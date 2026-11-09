package gateway

import (
	"reflect"
	"testing"
)

func TestVisionCapabilitiesAreStrictAndBackwardCompatible(t *testing.T) {
	for _, raw := range []string{"null", "[]", `{"auto":null}`, `{"auto":"true"}`} {
		if _, err := ParseVisionCapabilities(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if got, err := ParseVisionCapabilities(""); got != nil || err != nil {
		t.Fatal("legacy defaults changed")
	}
	cfg := Config{LLMVision: map[string]bool{"pool-other-user": true}}
	got, err := ResolveVisionCapabilities(cfg, CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_LLM_VISION": `{"auto":false}`}})
	if err != nil || len(got) != 1 || got["auto"] {
		t.Fatal("request capabilities did not replace pool defaults")
	}
	if got := VisionInputModalities([]string{"text", "audio", "image/png"}, false); !reflect.DeepEqual(got, []any{"text", "audio"}) {
		t.Fatal(got)
	}
}
