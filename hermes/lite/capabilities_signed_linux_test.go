//go:build linux

package hermes

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertDesktopFixtureRejected(t *testing.T, root string, release desktopRelease) {
	t.Helper()
	writeDesktopReleaseFixture(t, root, release)
	cfg := desktopCapabilityConfig(t)
	if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
		t.Fatal("invalid signed evidence advertised capability")
	}
}

func rewriteDesktopFixtureBinding(t *testing.T, root string, release *desktopRelease, binding desktopCampaignBinding) {
	t.Helper()
	release.Acceptance.Binding = writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"acceptance-binding.json", binding)
	for phase, ref := range release.Acceptance.Observations {
		var observation desktopCampaignObservation
		readDesktopFixtureJSON(t, root, ref.Path, &observation)
		observation.BindingSHA256 = release.Acceptance.Binding.SHA256
		release.Acceptance.Observations[phase] = writeDesktopFixtureJSON(t, root, ref.Path, observation)
	}
	var browser map[string]any
	readDesktopFixtureJSON(t, root, desktopEvidenceRoot+"cm-bff-browser-result.json", &browser)
	browser["binding_sha256"] = release.Acceptance.Binding.SHA256
	browserRef := writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"cm-bff-browser-result.json", browser)
	for _, reports := range []map[string]desktopReportRef{release.Acceptance.Reports, release.Acceptance.PromotionReports} {
		for suite, ref := range reports {
			var report desktopAcceptanceReport
			readDesktopFixtureJSON(t, root, ref.Path, &report)
			report.BindingSHA256 = release.Acceptance.Binding.SHA256
			if suite == "cm_bff_browser" {
				report.BrowserResult = &browserRef
			}
			reports[suite] = writeDesktopFixtureJSON(t, root, ref.Path, report)
		}
	}
	signDesktopFixture(t, root, release)
}

func TestDesktopCapabilityRejectsInvalidBindingWithValidSignature(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*desktopCampaignBinding)
	}{
		{"missing CM image", func(b *desktopCampaignBinding) { b.CMImageDigest = "" }},
		{"different observed CM", func(b *desktopCampaignBinding) { b.CMImageDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"different runtime manifest", func(b *desktopCampaignBinding) { b.CandidateManifestDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"different renderer", func(b *desktopCampaignBinding) { b.RendererBuildInputSHA256 = strings.Repeat("0", 64) }},
		{"missing renderer artifact manifest", func(b *desktopCampaignBinding) { b.RendererAssetManifestSHA256 = "" }},
		{"wrong browser harness", func(b *desktopCampaignBinding) { b.HarnessSHA256 = strings.Repeat("0", 64) }},
		{"wrong campaign runner", func(b *desktopCampaignBinding) { b.CampaignRunnerSHA256 = strings.Repeat("0", 64) }},
		{"missing browser version", func(b *desktopCampaignBinding) { b.BrowserVersion = "" }},
		{"missing browser binary", func(b *desktopCampaignBinding) { b.BrowserSHA256 = "" }},
		{"missing Playwright", func(b *desktopCampaignBinding) { b.PlaywrightSHA256 = "" }},
		{"wrong scope", func(b *desktopCampaignBinding) { b.Scope = "local-stub" }},
		{"wrong protocol", func(b *desktopCampaignBinding) { b.Protocol = "unrestricted" }},
		{"new unrelated run", func(b *desktopCampaignBinding) { b.RunID = strings.Repeat("cd", 16) }},
		{"future campaign", func(b *desktopCampaignBinding) { b.StartedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }},
		{"near future campaign", func(b *desktopCampaignBinding) { b.StartedAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339) }},
		{"version contains whitespace", func(b *desktopCampaignBinding) { b.BrowserVersion = "146.0 injected" }},
		{"oversized version", func(b *desktopCampaignBinding) { b.NodeVersion = strings.Repeat("1", 81) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			var binding desktopCampaignBinding
			readDesktopFixtureJSON(t, root, release.Acceptance.Binding.Path, &binding)
			tc.edit(&binding)
			rewriteDesktopFixtureBinding(t, root, &release, binding)
			assertDesktopFixtureRejected(t, root, release)
		})
	}
}

func TestDesktopCapabilityRejectsFalseObservationWithValidSignature(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*desktopCampaignObservation)
	}{
		{"different CM rollout", func(o *desktopCampaignObservation) { o.CMImageDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"different Runtime payload", func(o *desktopCampaignObservation) { o.RuntimePayloadSHA256 = strings.Repeat("0", 64) }},
		{"different browser resources", func(o *desktopCampaignObservation) { o.RendererAssetManifestSHA256 = strings.Repeat("0", 64) }},
		{"missing isolation", func(o *desktopCampaignObservation) { delete(o.Checks, "isolation") }},
		{"failed identity", func(o *desktopCampaignObservation) { o.Checks["cm_identity"] = "failed" }},
		{"unknown check", func(o *desktopCampaignObservation) { o.Checks["guessed"] = "passed" }},
		{"wrong phase", func(o *desktopCampaignObservation) { o.Phase = "before" }},
		{"observation before campaign", func(o *desktopCampaignObservation) { o.RecordedAt = "2026-01-01T00:00:00Z" }},
		{"future observation", func(o *desktopCampaignObservation) {
			o.RecordedAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			ref := release.Acceptance.Observations["after"]
			var observation desktopCampaignObservation
			readDesktopFixtureJSON(t, root, ref.Path, &observation)
			tc.edit(&observation)
			release.Acceptance.Observations["after"] = writeDesktopFixtureJSON(t, root, ref.Path, observation)
			signDesktopFixture(t, root, &release)
			assertDesktopFixtureRejected(t, root, release)
		})
	}
}

func TestDesktopCapabilityRejectsUntrustedSignedEvidenceFiles(t *testing.T) {
	for _, name := range []string{"acceptance-binding.json", "campaign-signature.json", "browser-artifacts/fixture.txt", "promotion-desktop_rpc.log"} {
		for _, mode := range []string{"symlink", "writable", "nonroot"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				root, release := desktopAcceptedReleaseFixture(t)
				file := filepath.Join(root, strings.TrimPrefix(desktopEvidenceRoot+name, "/"))
				var err error
				switch mode {
				case "symlink":
					if err = os.Rename(file, file+".real"); err == nil {
						err = os.Symlink(file+".real", file)
					}
				case "writable":
					err = os.Chmod(file, 0666)
				case "nonroot":
					err = os.Chown(file, 200001, 200001)
				}
				if err != nil {
					t.Fatal(err)
				}
				assertDesktopFixtureRejected(t, root, release)
			})
		}
	}
}

func TestDesktopCapabilityRequiresActualPromotionEvidence(t *testing.T) {
	for _, name := range []string{"missing", "extra CM", "failed", "old schema", "wrong phase", "wrong runner", "missing log", "changed log", "report before campaign"} {
		t.Run(name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			ref := release.Acceptance.PromotionReports["desktop_rpc"]
			var report desktopAcceptanceReport
			readDesktopFixtureJSON(t, root, ref.Path, &report)
			switch name {
			case "missing":
				delete(release.Acceptance.PromotionReports, "desktop_rpc")
			case "extra CM":
				release.Acceptance.PromotionReports["cm_bff_browser"] = release.Acceptance.Reports["cm_bff_browser"]
			case "failed":
				report.Status = "failed"
			case "old schema":
				report.SchemaVersion = 1
			case "wrong phase":
				report.Phase = "campaign"
			case "wrong runner":
				report.RunnerSHA256 = release.Artifacts[desktopShareRoot+"run_release_check.py"]
			case "missing log":
				_ = os.Remove(filepath.Join(root, strings.TrimPrefix(report.OutputPath, "/")))
			case "changed log":
				writeDesktopFixture(t, root, report.OutputPath, []byte("forged"))
			case "report before campaign":
				report.StartedAt = "2026-01-01T00:00:00Z"
			}
			if name != "missing" {
				release.Acceptance.PromotionReports["desktop_rpc"] = writeDesktopFixtureJSON(t, root, ref.Path, report)
			}
			assertDesktopFixtureRejected(t, root, release)
		})
	}
}

func TestDesktopCapabilityVerifiesSignatureNotJustHashes(t *testing.T) {
	for _, name := range []string{"wrong signature", "wrong key", "wrong key id", "wrong domain", "missing report", "foreign output", "duplicate payload key", "missing signature", "signature path escape"} {
		t.Run(name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			ref := release.Acceptance.Signature
			var envelope desktopCampaignEnvelope
			readDesktopFixtureJSON(t, root, ref.Path, &envelope)
			raw, _ := base64.StdEncoding.DecodeString(envelope.PayloadBase64)
			switch name {
			case "wrong signature":
				envelope.SignatureBase64 = base64.StdEncoding.EncodeToString(make([]byte, 64))
			case "wrong key":
				foreign := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{24}, 32))
				envelope.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(foreign, append([]byte(desktopSignatureDomain), raw...)))
			case "wrong key id":
				envelope.KeyID = "unknown-publisher"
			case "wrong domain":
				envelope.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(desktopFixtureKey(), raw))
			case "missing report", "foreign output":
				var payload desktopCampaignSignedPayload
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if name == "missing report" {
					delete(payload.Reports, "cm_bff_browser")
				} else {
					entry := payload.Reports["cm_bff_browser"]
					entry.OutputSHA256 = strings.Repeat("0", 64)
					payload.Reports["cm_bff_browser"] = entry
				}
				raw, _ = json.Marshal(payload)
				envelope.PayloadBase64 = base64.StdEncoding.EncodeToString(raw)
				envelope.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(desktopFixtureKey(), append([]byte(desktopSignatureDomain), raw...)))
			case "duplicate payload key":
				raw = append([]byte(`{"binding_sha256":"`+release.Acceptance.Binding.SHA256+`",`), raw[1:]...)
				envelope.PayloadBase64 = base64.StdEncoding.EncodeToString(raw)
				envelope.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(desktopFixtureKey(), append([]byte(desktopSignatureDomain), raw...)))
			case "missing signature":
				envelope.SignatureBase64 = ""
			}
			release.Acceptance.Signature = writeDesktopFixtureJSON(t, root, ref.Path, envelope)
			if name == "signature path escape" {
				release.Acceptance.Signature.Path = "/workspaces/forged.json"
			}
			assertDesktopFixtureRejected(t, root, release)
		})
	}
}

func TestDesktopCapabilityRequiresCompleteBrowserResultAndArtifacts(t *testing.T) {
	for _, name := range []string{"missing case", "failed case", "extra case", "foreign run", "empty artifacts", "path escape", "wrong size", "missing artifact", "changed artifact", "missing provenance", "changed renderer manifest"} {
		t.Run(name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			var report desktopAcceptanceReport
			ref := release.Acceptance.Reports["cm_bff_browser"]
			readDesktopFixtureJSON(t, root, ref.Path, &report)
			var result map[string]any
			readDesktopFixtureJSON(t, root, report.BrowserResult.Path, &result)
			switch name {
			case "missing case":
				delete(result["cases"].(map[string]any), "browser_origin_rejection")
			case "failed case":
				result["cases"].(map[string]any)["secret_surface_audit"] = "failed"
			case "extra case":
				result["cases"].(map[string]any)["guess"] = "passed"
			case "foreign run":
				result["run_id"] = strings.Repeat("cd", 16)
			case "empty artifacts":
				result["artifacts"] = map[string]any{}
			case "path escape":
				result["artifacts"] = map[string]any{"../fixture.txt": map[string]any{"sha256": strings.Repeat("c", 64), "size": 1}}
			case "wrong size":
				result["artifacts"].(map[string]any)["browser-artifacts/fixture.txt"].(map[string]any)["size"] = 1
			case "missing artifact":
				_ = os.Remove(filepath.Join(root, "usr/local/share/hermes-lite/evidence/browser-artifacts/fixture.txt"))
			case "changed artifact":
				writeDesktopFixture(t, root, desktopEvidenceRoot+"browser-artifacts/fixture.txt", []byte("changed"))
			case "missing provenance":
				_ = os.Remove(filepath.Join(root, "usr/local/share/hermes-lite/evidence/cm-provenance.json"))
			case "changed renderer manifest":
				writeDesktopFixture(t, root, desktopEvidenceRoot+"renderer-asset-manifest.json", []byte("changed"))
			}
			browserRef := writeDesktopFixtureJSON(t, root, report.BrowserResult.Path, result)
			report.BrowserResult = &browserRef
			release.Acceptance.Reports["cm_bff_browser"] = writeDesktopFixtureJSON(t, root, ref.Path, report)
			signDesktopFixture(t, root, &release)
			assertDesktopFixtureRejected(t, root, release)
		})
	}
}
