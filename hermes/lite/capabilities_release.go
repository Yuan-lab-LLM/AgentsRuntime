package hermes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

var desktopAcceptanceScripts = map[string]string{
	"lite_image": "smoke_lite_image.py", "lite_provider": "smoke_lite_provider.py", "lite_agent": "smoke_lite_agent.py",
	"lite_non_native": "smoke_lite_non_native.py",
	"desktop_rpc":     "smoke_lite_desktop_rpc.py", "cm_bff_browser": "smoke_lite_cm_bff_browser.py",
}

type desktopRelease struct {
	SchemaVersion   int                `json:"schema_version"`
	HermesRef       string             `json:"hermes_ref"`
	HermesCommit    string             `json:"hermes_commit"`
	PackageVersion  string             `json:"package_version"`
	NodeVersion     string             `json:"node_version"`
	ContractVersion int                `json:"contract_version"`
	RPCProtocol     string             `json:"rpc_protocol"`
	BackendMode     string             `json:"backend_mode"`
	AuthMode        string             `json:"auth_mode"`
	Accepted        bool               `json:"desktop_web_accepted"`
	Artifacts       map[string]string  `json:"artifacts"`
	PayloadSHA256   string             `json:"payload_sha256"`
	Acceptance      *desktopAcceptance `json:"acceptance"`
}
type desktopAcceptance struct {
	CandidateImageDigest string                      `json:"candidate_image_digest"`
	PayloadSHA256        string                      `json:"payload_sha256"`
	Reports              map[string]desktopReportRef `json:"reports"`
	Binding              desktopReportRef            `json:"binding"`
	Signature            desktopReportRef            `json:"signature"`
	Observations         map[string]desktopReportRef `json:"observations"`
	PromotionReports     map[string]desktopReportRef `json:"promotion_reports"`
}
type desktopReportRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type desktopAcceptanceReport struct {
	SchemaVersion        int               `json:"schema_version"`
	Suite                string            `json:"suite"`
	Status               string            `json:"status"`
	ExitCode             *int              `json:"exit_code"`
	CandidateImageDigest string            `json:"candidate_image_digest"`
	PayloadSHA256        string            `json:"payload_sha256"`
	ScriptPath           string            `json:"script_path"`
	ScriptSHA256         string            `json:"script_sha256"`
	RunnerSHA256         string            `json:"runner_sha256"`
	OutputSHA256         string            `json:"output_sha256"`
	OutputPath           string            `json:"output_path"`
	StartedAt            string            `json:"started_at"`
	FinishedAt           string            `json:"finished_at"`
	BindingSHA256        string            `json:"binding_sha256"`
	Phase                string            `json:"phase"`
	BrowserResult        *desktopReportRef `json:"browser_result,omitempty"`
}

// Protocol compatibility describes the managed runtime that is installed.
// Signed end-to-end release acceptance remains a separate, truthful state.
// An image claiming acceptance must still pass every existing signature gate.
func readVerifiedDesktopRelease(root *os.Root) (*desktopRelease, bool) {
	data, err := readDesktopCapabilityFile(root, desktopReleasePath, 4<<20)
	var release desktopRelease
	if err != nil || !decodeDesktopEvidenceJSON(data, &release) || release.SchemaVersion != 2 ||
		release.HermesRef != desktopWebHermesGitRef || release.HermesCommit != desktopWebHermesCommit || release.PackageVersion != desktopWebHermesVersion || release.NodeVersion != "22.23.2" ||
		release.ContractVersion != 1 || release.RPCProtocol != "hermes-jsonrpc-v1" || release.BackendMode != "serve" || release.AuthMode != "password-cookie" {
		return nil, false
	}
	// bool's zero value cannot distinguish an explicit false from omitted/null.
	var state struct {
		Accepted *bool `json:"desktop_web_accepted"`
	}
	if json.Unmarshal(data, &state) != nil || state.Accepted == nil ||
		(release.Accepted && release.Acceptance == nil) || (!release.Accepted && release.Acceptance != nil) {
		return nil, false
	}
	if len(release.Artifacts) == 0 || len(release.Artifacts) > 16384 || !desktopSHA256(release.PayloadSHA256) {
		return nil, false
	}
	for _, required := range desktopRequiredArtifacts {
		if !desktopSHA256(release.Artifacts[required]) {
			return nil, false
		}
	}
	var paths []string
	for file, digest := range release.Artifacts {
		if !desktopArtifactPath(file) || !desktopSHA256(digest) {
			return nil, false
		}
		paths = append(paths, file)
	}
	sort.Strings(paths)
	payload := sha256.New()
	for _, file := range paths {
		_, _ = io.WriteString(payload, file+"\x00"+release.Artifacts[file]+"\n")
	}
	if hex.EncodeToString(payload.Sum(nil)) != release.PayloadSHA256 {
		return nil, false
	}
	if release.Accepted && !verifyDesktopAcceptance(root, &release) {
		return nil, false
	}
	// Hash every actual image-owned artifact before claiming compatibility. This
	// bounded one-time work is never performed by health reads.
	var totalBytes int64
	for _, file := range paths {
		opened, err := openDesktopCapabilityFile(root, strings.TrimPrefix(file, "/"), 256<<20)
		if err != nil {
			return nil, false
		}
		if strings.HasPrefix(file, "/usr/local/bin/") || file == "/opt/hermes-agent/.venv/bin/hermes" {
			info, err := opened.Stat()
			if err != nil || info.Mode().Perm()&0111 == 0 {
				opened.Close()
				return nil, false
			}
		}
		hash := sha256.New()
		count, err := io.Copy(hash, io.LimitReader(opened, (256<<20)+1))
		_ = opened.Close()
		totalBytes += count
		if err != nil || count > 256<<20 || totalBytes > 1<<30 || hex.EncodeToString(hash.Sum(nil)) != release.Artifacts[file] {
			return nil, false
		}
	}
	return &release, true
}

func desktopArtifactPath(file string) bool {
	if path.Clean(file) != file || strings.ContainsAny(file, "\\\r\n\x00") {
		return false
	}
	if file == HermesManagedProxyPolicyMarker {
		return true
	}
	for _, prefix := range []string{"/opt/hermes-agent/", "/usr/local/lib/python3.13/", "/usr/local/share/hermes-lite/patches/", "/usr/local/share/hermes-lite/tests/"} {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	for _, required := range desktopRequiredArtifacts {
		if file == required {
			return true
		}
	}
	return false
}
func desktopSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
func desktopDigest(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

// Keep this list aligned with the release assembler; additional artifacts are
// welcome, but a manifest cannot omit the code/launcher that enforces the mode.
var desktopRequiredArtifacts = []string{
	"/opt/hermes-agent/plugins/platforms/redis_team/adapter.py", "/opt/hermes-agent/plugins/platforms/redis_team/plugin.yaml",
	"/usr/local/share/hermes-lite/patches/apply_team_completion_stop.py", "/usr/local/share/hermes-lite/patches/apply_profile_external_skill_dedupe.py",
	"/usr/local/bin/start-hermes-lite-runtime", "/usr/local/bin/hermes-apply-runtime-config",
	"/usr/local/share/clawmanager/hermes/skills/redis-team-protocol/SKILL.md", "/usr/local/share/clawmanager/hermes/skills/redis-team-protocol/skill.json",
	"/usr/local/bin/start-hermes-lite-backend", "/usr/local/bin/clawmanager-agent", "/usr/local/bin/node", "/usr/local/bin/hermes-lite-entrypoint", "/usr/local/bin/python3.13",
	"/usr/local/lib/libpython3.13.so.1.0",
	"/opt/hermes-agent/hermes_cli/web_server.py", "/opt/hermes-agent/hermes_cli/runtime_provider.py", "/opt/hermes-agent/hermes_cli/lite_gateway_boundary.py", "/opt/hermes-agent/hermes_cli/lite_non_native.py", "/opt/hermes-agent/tui_gateway/server.py", "/opt/hermes-agent/tui_gateway/methods_session.py", "/opt/hermes-agent/model_tools.py",
	"/opt/hermes-agent/hermes_cli/tui_dist/entry.js",
	"/opt/hermes-agent/agent/tool_executor.py",
	"/opt/hermes-agent/hermes_cli/env_loader.py", "/opt/hermes-agent/hermes_cli/lite_environment.py",
	"/opt/hermes-agent/.venv/bin/hermes", "/opt/hermes-agent/pyproject.toml", "/opt/hermes-agent/uv.lock", "/opt/hermes-agent/package-lock.json",
	"/usr/local/share/hermes-lite/source-lock.json", "/usr/local/share/hermes-lite/verify_lite_release.py", "/usr/local/share/hermes-lite/run_release_check.py",
	"/usr/local/share/hermes-lite/patches/apply_lite_gateway_boundary.py", "/usr/local/share/hermes-lite/patches/lite_gateway_boundary.py", "/usr/local/share/hermes-lite/patches/apply_lite_non_native.py", "/usr/local/share/hermes-lite/patches/lite_non_native.py",
	"/usr/local/share/hermes-lite/patches/apply_lite_tool_executor.py", "/usr/local/share/hermes-lite/tests/check_lite_tool_executor.py",
	"/usr/local/share/hermes-lite/patches/lite_environment.py",
	"/usr/local/share/hermes-lite/release-trust.json", "/usr/local/share/hermes-lite/acceptance-protocol.json",
	"/usr/local/share/hermes-lite/verify_campaign_signature.mjs", "/usr/local/share/hermes-lite/campaign_signing.py",
	"/usr/local/share/hermes-lite/run_campaign.py", "/usr/local/share/hermes-lite/tests/cm_bff_browser.mjs",
	"/usr/local/share/hermes-lite/campaign_evidence.py", "/usr/local/share/hermes-lite/tests/acceptance_model_stub.py",
	"/usr/local/share/hermes-lite/verify_lite_promotion_oci.py",
}
