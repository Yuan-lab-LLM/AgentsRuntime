package hermes

import (
	"strings"
	"testing"
)

func TestDesktopEvidenceJSONRejectsAmbiguousFields(t *testing.T) {
	for _, body := range []string{
		`{"binding_sha256":"a","binding_sha256":"b"}`,
		`{"binding_sha256":"a","Binding_SHA256":"b"}`,
		`{"BINDING_SHA256":"a"}`,
		`{"reports":{"desktop_rpc":{"SHA256":"a","output_sha256":"b"}}}`,
		`{"reports":{"desktop_rpc":{"sha256":"a","sha256":"b"}}}`,
		`{"unexpected":true}`,
		`{} {}`,
		`{"binding_sha256":"` + string([]byte{0xff}) + `"}`,
		strings.Repeat(`[`, 65) + `0` + strings.Repeat(`]`, 65),
	} {
		var target desktopCampaignSignedPayload
		if decodeDesktopEvidenceJSON([]byte(body), &target) {
			t.Fatalf("ambiguous evidence decoded: %q", body)
		}
	}
	var target desktopCampaignSignedPayload
	if !decodeDesktopEvidenceJSON([]byte(`{"binding_sha256":"a","reports":{"desktop_rpc":{"sha256":"b","output_sha256":"c"}},"observations":{"before_sha256":"d","after_sha256":"e"}}`), &target) {
		t.Fatal("exact evidence fields rejected")
	}
}
