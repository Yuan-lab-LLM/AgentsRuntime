package hermes

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

const desktopEvidenceRoot = "/usr/local/share/hermes-lite/evidence/"
const desktopShareRoot = "/usr/local/share/hermes-lite/"
const desktopCampaignProtocol = "hermes-lite-campaign-v1"
const desktopSignatureDomain = "hermes-lite-acceptance-v1\n"

var desktopCampaignSuites = []string{"lite_image", "lite_provider", "lite_agent", "lite_non_native", "desktop_rpc", "cm_bff_browser"}
var desktopObservationChecks = []string{"runtime_identity", "cm_identity", "renderer_identity", "isolation"}
var desktopBrowserChecks = []string{
	"candidate_admission_identity", "cm_build_renderer_resources", "browser_login_ownership", "desktop_boot_original_dom",
	"cm_cookie_scope", "http_contract", "browser_origin_rejection", "ws_single_use_identity", "ui_prompt_stream_history",
	"rpc_session_lifecycle_reconnect", "interrupt", "approval_once", "approval_deny", "clarify", "three_replica_tickets",
	"lease_renewal_logout", "secret_surface_audit", "stub_only_observation",
}

type desktopCampaignBinding struct {
	SchemaVersion               int    `json:"schema_version"`
	Protocol                    string `json:"protocol"`
	Scope                       string `json:"scope"`
	RunID                       string `json:"run_id"`
	StartedAt                   string `json:"started_at"`
	CandidateImageDigest        string `json:"candidate_image_digest"`
	CandidateManifestDigest     string `json:"candidate_manifest_digest"`
	CandidateConfigDigest       string `json:"candidate_config_digest"`
	CMImageDigest               string `json:"cm_image_digest"`
	PayloadSHA256               string `json:"payload_sha256"`
	CMProvenanceSHA256          string `json:"cm_provenance_sha256"`
	RendererBuildInputSHA256    string `json:"renderer_build_input_sha256"`
	RendererAssetManifestSHA256 string `json:"renderer_asset_manifest_sha256"`
	HarnessSHA256               string `json:"harness_sha256"`
	CampaignRunnerSHA256        string `json:"campaign_runner_sha256"`
	ExecutionConfigSHA256       string `json:"execution_config_sha256"`
	NodeSHA256                  string `json:"node_sha256"`
	BrowserSHA256               string `json:"browser_sha256"`
	PlaywrightSHA256            string `json:"playwright_sha256"`
	NodeVersion                 string `json:"node_version"`
	BrowserVersion              string `json:"browser_version"`
	PlaywrightVersion           string `json:"playwright_version"`
}

type desktopCampaignObservation struct {
	SchemaVersion               int               `json:"schema_version"`
	Phase                       string            `json:"phase"`
	Status                      string            `json:"status"`
	RunID                       string            `json:"run_id"`
	BindingSHA256               string            `json:"binding_sha256"`
	RuntimeImageDigest          string            `json:"runtime_image_digest"`
	RuntimePayloadSHA256        string            `json:"runtime_payload_sha256"`
	CMImageDigest               string            `json:"cm_image_digest"`
	RendererBuildInputSHA256    string            `json:"renderer_build_input_sha256"`
	RendererAssetManifestSHA256 string            `json:"renderer_asset_manifest_sha256"`
	RecordedAt                  string            `json:"recorded_at"`
	Checks                      map[string]string `json:"checks"`
}

type desktopCampaignTrust struct {
	SchemaVersion   int    `json:"schema_version"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PublicKeyBase64 string `json:"public_key_base64"`
	KeyOrigin       string `json:"key_origin"`
}

type desktopCampaignEnvelope struct {
	SchemaVersion   int    `json:"schema_version"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PayloadBase64   string `json:"payload_base64"`
	SignatureBase64 string `json:"signature_base64"`
}

type desktopCampaignSignedReport struct {
	SHA256       string `json:"sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type desktopCampaignSignedPayload struct {
	BindingSHA256 string                                 `json:"binding_sha256"`
	Reports       map[string]desktopCampaignSignedReport `json:"reports"`
	Observations  struct {
		BeforeSHA256 string `json:"before_sha256"`
		AfterSHA256  string `json:"after_sha256"`
	} `json:"observations"`
}

type desktopAcceptanceProtocol struct {
	SchemaVersion       int      `json:"schema_version"`
	Protocol            string   `json:"protocol"`
	ReportSchemaVersion int      `json:"report_schema_version"`
	Suites              []string `json:"suites"`
	PromotionSuites     []string `json:"promotion_suites"`
	ObservationChecks   []string `json:"observation_checks"`
	BrowserChecks       []string `json:"browser_checks"`
}

func readDesktopDescriptor(root *os.Root, ref desktopReportRef, expected string, limit int64) ([]byte, bool) {
	if ref.Path != expected || !desktopSHA256(ref.SHA256) {
		return nil, false
	}
	body, err := readDesktopCapabilityFile(root, strings.TrimPrefix(expected, "/"), limit)
	return body, err == nil && desktopDigest(body) == ref.SHA256
}

func desktopImageDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && desktopSHA256(strings.TrimPrefix(value, "sha256:"))
}

func desktopCampaignTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && !parsed.IsZero() && !parsed.After(time.Now().Add(5*time.Minute))
}

func desktopPassedChecks(checks map[string]string, expected []string) bool {
	if len(checks) != len(expected) {
		return false
	}
	for _, name := range expected {
		if checks[name] != "passed" {
			return false
		}
	}
	return true
}

func verifyDesktopCampaignBinding(binding desktopCampaignBinding, release *desktopRelease) bool {
	if binding.SchemaVersion != 1 || binding.Protocol != desktopCampaignProtocol || binding.Scope != "real-cm-bff-browser" ||
		binding.CandidateImageDigest != release.Acceptance.CandidateImageDigest || binding.PayloadSHA256 != release.PayloadSHA256 ||
		binding.HarnessSHA256 != release.Artifacts[desktopShareRoot+"tests/cm_bff_browser.mjs"] ||
		binding.CampaignRunnerSHA256 != release.Artifacts[desktopShareRoot+"run_campaign.py"] {
		return false
	}
	id, err := hex.DecodeString(binding.RunID)
	if err != nil || len(id) != 16 || strings.ToLower(binding.RunID) != binding.RunID {
		return false
	}
	for _, value := range []string{binding.CandidateImageDigest, binding.CandidateManifestDigest, binding.CandidateConfigDigest, binding.CMImageDigest} {
		if !desktopImageDigest(value) {
			return false
		}
	}
	for _, value := range []string{binding.PayloadSHA256, binding.CMProvenanceSHA256, binding.RendererBuildInputSHA256, binding.RendererAssetManifestSHA256, binding.HarnessSHA256, binding.CampaignRunnerSHA256, binding.ExecutionConfigSHA256, binding.NodeSHA256, binding.BrowserSHA256, binding.PlaywrightSHA256} {
		if !desktopSHA256(value) {
			return false
		}
	}
	for _, version := range []string{binding.NodeVersion, binding.BrowserVersion, binding.PlaywrightVersion} {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,79}$`).MatchString(version) {
			return false
		}
	}
	started, ok := desktopCampaignTime(binding.StartedAt)
	return ok && !started.After(time.Now())
}

func verifyDesktopCampaignObservation(observation desktopCampaignObservation, phase string, binding desktopCampaignBinding, bindingHash string) bool {
	if observation.SchemaVersion != 1 || observation.Phase != phase || observation.Status != "passed" || observation.RunID != binding.RunID ||
		observation.BindingSHA256 != bindingHash || observation.RuntimeImageDigest != binding.CandidateManifestDigest || observation.RuntimePayloadSHA256 != binding.PayloadSHA256 ||
		observation.CMImageDigest != binding.CMImageDigest || observation.RendererBuildInputSHA256 != binding.RendererBuildInputSHA256 || observation.RendererAssetManifestSHA256 != binding.RendererAssetManifestSHA256 ||
		!desktopPassedChecks(observation.Checks, desktopObservationChecks) {
		return false
	}
	recorded, ok := desktopCampaignTime(observation.RecordedAt)
	started, _ := desktopCampaignTime(binding.StartedAt)
	return ok && !recorded.Before(started) && !recorded.After(time.Now())
}

func readDesktopExecutionReport(root *os.Root, release *desktopRelease, suite, phase string, ref desktopReportRef) (desktopAcceptanceReport, bool) {
	prefix, runner := "", "run_release_check.py"
	if phase == "promotion" {
		prefix, runner = "promotion-", "verify_lite_release.py"
	}
	var report desktopAcceptanceReport
	body, ok := readDesktopDescriptor(root, ref, desktopEvidenceRoot+prefix+suite+".json", 64<<10)
	if !ok || !decodeDesktopEvidenceJSON(body, &report) || report.SchemaVersion != 2 || report.Suite != suite || report.Phase != phase || report.Status != "passed" || report.ExitCode == nil || *report.ExitCode != 0 ||
		report.CandidateImageDigest != release.Acceptance.CandidateImageDigest || report.PayloadSHA256 != release.PayloadSHA256 || report.BindingSHA256 != release.Acceptance.Binding.SHA256 ||
		report.ScriptPath != desktopShareRoot+"tests/"+desktopAcceptanceScripts[suite] || report.ScriptSHA256 != release.Artifacts[report.ScriptPath] || !desktopSHA256(report.ScriptSHA256) ||
		report.RunnerSHA256 != release.Artifacts[desktopShareRoot+runner] || !desktopSHA256(report.RunnerSHA256) || !desktopSHA256(report.OutputSHA256) ||
		report.OutputPath != desktopEvidenceRoot+prefix+suite+".log" {
		return report, false
	}
	started, startOK := desktopCampaignTime(report.StartedAt)
	finished, finishOK := desktopCampaignTime(report.FinishedAt)
	if !startOK || !finishOK || finished.Before(started) {
		return report, false
	}
	_, ok = readDesktopDescriptor(root, desktopReportRef{Path: report.OutputPath, SHA256: report.OutputSHA256}, report.OutputPath, 8<<20)
	return report, ok
}

func verifyDesktopCampaignSignature(root *os.Root, release *desktopRelease, reports map[string]desktopAcceptanceReport) bool {
	var trust desktopCampaignTrust
	body, err := readDesktopCapabilityFile(root, strings.TrimPrefix(desktopShareRoot+"release-trust.json", "/"), 64<<10)
	if err != nil || desktopDigest(body) != release.Artifacts[desktopShareRoot+"release-trust.json"] || !decodeDesktopEvidenceJSON(body, &trust) || trust.SchemaVersion != 1 || trust.Algorithm != "Ed25519" ||
		trust.KeyOrigin != "local-operator" || !regexp.MustCompile(`^[a-z0-9-]{1,64}$`).MatchString(trust.KeyID) {
		return false
	}
	var envelope desktopCampaignEnvelope
	body, ok := readDesktopDescriptor(root, release.Acceptance.Signature, desktopEvidenceRoot+"campaign-signature.json", 64<<10)
	if !ok || !decodeDesktopEvidenceJSON(body, &envelope) || envelope.SchemaVersion != 1 || envelope.Algorithm != "Ed25519" || envelope.KeyID != trust.KeyID {
		return false
	}
	for _, encoded := range []string{trust.PublicKeyBase64, envelope.PayloadBase64, envelope.SignatureBase64} {
		if strings.ContainsAny(encoded, " \t\r\n") {
			return false
		}
	}
	key, e1 := base64.StdEncoding.Strict().DecodeString(trust.PublicKeyBase64)
	payload, e2 := base64.StdEncoding.Strict().DecodeString(envelope.PayloadBase64)
	signature, e3 := base64.StdEncoding.Strict().DecodeString(envelope.SignatureBase64)
	if e1 != nil || e2 != nil || e3 != nil || len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || len(payload) > 64<<10 {
		return false
	}
	var signed desktopCampaignSignedPayload
	if !decodeDesktopEvidenceJSON(payload, &signed) || signed.BindingSHA256 != release.Acceptance.Binding.SHA256 || len(signed.Reports) != len(desktopCampaignSuites) ||
		signed.Observations.BeforeSHA256 != release.Acceptance.Observations["before"].SHA256 || signed.Observations.AfterSHA256 != release.Acceptance.Observations["after"].SHA256 {
		return false
	}
	for suite, report := range reports {
		entry, ok := signed.Reports[suite]
		if !ok || entry.SHA256 != release.Acceptance.Reports[suite].SHA256 || entry.OutputSHA256 != report.OutputSHA256 {
			return false
		}
	}
	message := append([]byte(desktopSignatureDomain), payload...)
	return ed25519.Verify(ed25519.PublicKey(key), message, signature)
}

func verifyDesktopAcceptance(root *os.Root, release *desktopRelease) bool {
	acceptance := release.Acceptance
	if acceptance.PayloadSHA256 != release.PayloadSHA256 || !desktopImageDigest(acceptance.CandidateImageDigest) || len(acceptance.Reports) != 6 || len(acceptance.PromotionReports) != 5 || len(acceptance.Observations) != 2 {
		return false
	}
	var protocol desktopAcceptanceProtocol
	body, err := readDesktopCapabilityFile(root, strings.TrimPrefix(desktopShareRoot+"acceptance-protocol.json", "/"), 64<<10)
	if err != nil || desktopDigest(body) != release.Artifacts[desktopShareRoot+"acceptance-protocol.json"] || !decodeDesktopEvidenceJSON(body, &protocol) ||
		protocol.SchemaVersion != 1 || protocol.Protocol != desktopCampaignProtocol || protocol.ReportSchemaVersion != 2 || !slices.Equal(protocol.Suites, desktopCampaignSuites) ||
		!slices.Equal(protocol.PromotionSuites, desktopCampaignSuites[:5]) || !slices.Equal(protocol.ObservationChecks, desktopObservationChecks) || !slices.Equal(protocol.BrowserChecks, desktopBrowserChecks) {
		return false
	}
	var binding desktopCampaignBinding
	body, ok := readDesktopDescriptor(root, acceptance.Binding, desktopEvidenceRoot+"acceptance-binding.json", 64<<10)
	if !ok || !decodeDesktopEvidenceJSON(body, &binding) || !verifyDesktopCampaignBinding(binding, release) {
		return false
	}
	for name, digest := range map[string]string{"cm-provenance.json": binding.CMProvenanceSHA256, "renderer-asset-manifest.json": binding.RendererAssetManifestSHA256} {
		if _, ok := readDesktopDescriptor(root, desktopReportRef{Path: desktopEvidenceRoot + name, SHA256: digest}, desktopEvidenceRoot+name, 1<<20); !ok {
			return false
		}
	}
	var before, after desktopCampaignObservation
	for phase, target := range map[string]*desktopCampaignObservation{"before": &before, "after": &after} {
		body, ok := readDesktopDescriptor(root, acceptance.Observations[phase], desktopEvidenceRoot+"campaign-"+phase+".json", 64<<10)
		if !ok || !decodeDesktopEvidenceJSON(body, target) || !verifyDesktopCampaignObservation(*target, phase, binding, acceptance.Binding.SHA256) {
			return false
		}
	}
	beforeTime, _ := desktopCampaignTime(before.RecordedAt)
	afterTime, _ := desktopCampaignTime(after.RecordedAt)
	if afterTime.Before(beforeTime) {
		return false
	}
	reports := map[string]desktopAcceptanceReport{}
	for _, suite := range desktopCampaignSuites {
		report, ok := readDesktopExecutionReport(root, release, suite, "campaign", acceptance.Reports[suite])
		if !ok {
			return false
		}
		started, _ := desktopCampaignTime(report.StartedAt)
		finished, _ := desktopCampaignTime(report.FinishedAt)
		if started.Before(beforeTime) || finished.After(afterTime) {
			return false
		}
		if suite == "cm_bff_browser" {
			if !verifyDesktopBrowserResult(root, report.BrowserResult, binding, acceptance.Binding.SHA256) {
				return false
			}
		} else if report.BrowserResult != nil {
			return false
		}
		reports[suite] = report
	}
	if !verifyDesktopCampaignSignature(root, release, reports) {
		return false
	}
	for _, suite := range desktopCampaignSuites[:5] {
		report, ok := readDesktopExecutionReport(root, release, suite, "promotion", acceptance.PromotionReports[suite])
		if !ok || report.BrowserResult != nil {
			return false
		}
		started, _ := desktopCampaignTime(report.StartedAt)
		if started.Before(afterTime) {
			return false
		}
	}
	return true
}
