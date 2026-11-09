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

// Deliberately public test-only key. These synthetic records never attest a run.
func desktopFixtureKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
}

func writeDesktopFixtureJSON(t *testing.T, root, name string, value any) desktopReportRef {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeDesktopFixture(t, root, name, body)
	return desktopReportRef{Path: name, SHA256: desktopDigest(body)}
}

func readDesktopFixtureJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(name, "/")))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, value); err != nil {
		t.Fatal(err)
	}
}

func desktopFixtureChecks(names []string) map[string]string {
	checks := map[string]string{}
	for _, name := range names {
		checks[name] = "passed"
	}
	return checks
}

func signDesktopFixture(t *testing.T, root string, release *desktopRelease) {
	t.Helper()
	payload := desktopCampaignSignedPayload{BindingSHA256: release.Acceptance.Binding.SHA256, Reports: map[string]desktopCampaignSignedReport{}}
	payload.Observations.BeforeSHA256 = release.Acceptance.Observations["before"].SHA256
	payload.Observations.AfterSHA256 = release.Acceptance.Observations["after"].SHA256
	for suite, ref := range release.Acceptance.Reports {
		var report desktopAcceptanceReport
		readDesktopFixtureJSON(t, root, ref.Path, &report)
		payload.Reports[suite] = desktopCampaignSignedReport{SHA256: ref.SHA256, OutputSHA256: report.OutputSHA256}
	}
	raw, _ := json.Marshal(payload)
	envelope := desktopCampaignEnvelope{SchemaVersion: 1, Algorithm: "Ed25519", KeyID: "unit-test-only", PayloadBase64: base64.StdEncoding.EncodeToString(raw), SignatureBase64: base64.StdEncoding.EncodeToString(ed25519.Sign(desktopFixtureKey(), append([]byte(desktopSignatureDomain), raw...)))}
	release.Acceptance.Signature = writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"campaign-signature.json", envelope)
}

func writeDesktopCampaignFixture(t *testing.T, root string, release *desktopRelease) {
	t.Helper()
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	stamp := func(offset time.Duration) string { return base.Add(offset).Format(time.RFC3339) }
	hash := strings.Repeat("c", 64)
	acceptance := &desktopAcceptance{CandidateImageDigest: "sha256:" + strings.Repeat("b", 64), PayloadSHA256: release.PayloadSHA256, Reports: map[string]desktopReportRef{}, PromotionReports: map[string]desktopReportRef{}, Observations: map[string]desktopReportRef{}}
	release.Acceptance = acceptance
	provenance := writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"cm-provenance.json", map[string]string{"fixture": "not production provenance"})
	renderer := writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"renderer-asset-manifest.json", map[string]string{"fixture": "not production renderer"})
	binding := desktopCampaignBinding{SchemaVersion: 1, Protocol: desktopCampaignProtocol, Scope: "real-cm-bff-browser", RunID: strings.Repeat("ab", 16), StartedAt: stamp(0), CandidateImageDigest: acceptance.CandidateImageDigest, CandidateManifestDigest: "sha256:" + strings.Repeat("d", 64), CandidateConfigDigest: "sha256:" + strings.Repeat("e", 64), CMImageDigest: "sha256:" + strings.Repeat("f", 64), PayloadSHA256: release.PayloadSHA256, CMProvenanceSHA256: provenance.SHA256, RendererBuildInputSHA256: hash, RendererAssetManifestSHA256: renderer.SHA256, HarnessSHA256: release.Artifacts[desktopShareRoot+"tests/cm_bff_browser.mjs"], CampaignRunnerSHA256: release.Artifacts[desktopShareRoot+"run_campaign.py"], ExecutionConfigSHA256: hash, NodeSHA256: hash, BrowserSHA256: hash, PlaywrightSHA256: hash, NodeVersion: "22.23.2", BrowserVersion: "148.0.1.0", PlaywrightVersion: "1.58.2"}
	acceptance.Binding = writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"acceptance-binding.json", binding)
	for phase, offset := range map[string]time.Duration{"before": time.Minute, "after": 4 * time.Minute} {
		observation := desktopCampaignObservation{SchemaVersion: 1, Phase: phase, Status: "passed", RunID: binding.RunID, BindingSHA256: acceptance.Binding.SHA256, RuntimeImageDigest: binding.CandidateManifestDigest, RuntimePayloadSHA256: binding.PayloadSHA256, CMImageDigest: binding.CMImageDigest, RendererBuildInputSHA256: binding.RendererBuildInputSHA256, RendererAssetManifestSHA256: binding.RendererAssetManifestSHA256, RecordedAt: stamp(offset), Checks: desktopFixtureChecks(desktopObservationChecks)}
		acceptance.Observations[phase] = writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"campaign-"+phase+".json", observation)
	}
	artifactName := "browser-artifacts/fixture.txt"
	artifact := []byte("synthetic browser fixture; no executed browser evidence")
	writeDesktopFixture(t, root, desktopEvidenceRoot+artifactName, artifact)
	artifactSize := int64(len(artifact))
	browserResult := writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+"cm-bff-browser-result.json", map[string]any{"schema_version": 1, "status": "passed", "run_id": binding.RunID, "binding_sha256": acceptance.Binding.SHA256, "cases": desktopFixtureChecks(desktopBrowserChecks), "artifacts": map[string]desktopBrowserArtifact{artifactName: {SHA256: desktopDigest(artifact), Size: &artifactSize}}, "fixture": "synthetic only"})
	for _, phase := range []string{"campaign", "promotion"} {
		prefix, runner := "", "run_release_check.py"
		start, finish := 2*time.Minute, 3*time.Minute
		if phase == "promotion" {
			prefix, runner = "promotion-", "verify_lite_release.py"
			start, finish = 5*time.Minute, 6*time.Minute
		}
		for _, suite := range desktopCampaignSuites {
			if phase == "promotion" && suite == "cm_bff_browser" {
				continue
			}
			zero := 0
			report := desktopAcceptanceReport{SchemaVersion: 2, Suite: suite, Phase: phase, Status: "passed", ExitCode: &zero, CandidateImageDigest: acceptance.CandidateImageDigest, PayloadSHA256: release.PayloadSHA256, BindingSHA256: acceptance.Binding.SHA256, ScriptPath: desktopShareRoot + "tests/" + desktopAcceptanceScripts[suite], RunnerSHA256: release.Artifacts[desktopShareRoot+runner], OutputPath: desktopEvidenceRoot + prefix + suite + ".log", OutputSHA256: desktopDigest([]byte("synthetic output")), StartedAt: stamp(start), FinishedAt: stamp(finish)}
			report.ScriptSHA256 = release.Artifacts[report.ScriptPath]
			if suite == "cm_bff_browser" {
				report.BrowserResult = &browserResult
			}
			writeDesktopFixture(t, root, report.OutputPath, []byte("synthetic output"))
			ref := writeDesktopFixtureJSON(t, root, desktopEvidenceRoot+prefix+suite+".json", report)
			if phase == "promotion" {
				acceptance.PromotionReports[suite] = ref
			} else {
				acceptance.Reports[suite] = ref
			}
		}
	}
	signDesktopFixture(t, root, release)
}
