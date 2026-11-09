package hermes

import (
	"encoding/json"
	"os"
	"regexp"
)

type desktopBrowserArtifact struct {
	SHA256 string `json:"sha256"`
	Size   *int64 `json:"size"`
}

func verifyDesktopBrowserResult(root *os.Root, ref *desktopReportRef, binding desktopCampaignBinding, bindingHash string) bool {
	if ref == nil {
		return false
	}
	body, ok := readDesktopDescriptor(root, *ref, desktopEvidenceRoot+"cm-bff-browser-result.json", 1<<20)
	if !ok {
		return false
	}
	// Diagnostic details are allowed, but required fields are decoded by their
	// exact spelling. They cannot be shadowed by case-insensitive Go field names.
	var fields map[string]json.RawMessage
	if !decodeDesktopEvidenceJSON(body, &fields) {
		return false
	}
	var schema int
	var status, runID, observedBinding string
	var cases map[string]string
	var artifacts map[string]desktopBrowserArtifact
	for key, target := range map[string]any{"schema_version": &schema, "status": &status, "run_id": &runID, "binding_sha256": &observedBinding, "cases": &cases, "artifacts": &artifacts} {
		if raw, present := fields[key]; !present || !decodeDesktopEvidenceJSON(raw, target) {
			return false
		}
	}
	if schema != 1 || status != "passed" || runID != binding.RunID || observedBinding != bindingHash || !desktopPassedChecks(cases, desktopBrowserChecks) || len(artifacts) < 1 || len(artifacts) > 32 {
		return false
	}
	var total int64
	for name, entry := range artifacts {
		if !regexp.MustCompile(`^browser-artifacts/[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`).MatchString(name) || entry.Size == nil || *entry.Size < 0 || *entry.Size > 8<<20 {
			return false
		}
		total += *entry.Size
		if total > 32<<20 {
			return false
		}
		body, ok := readDesktopDescriptor(root, desktopReportRef{Path: desktopEvidenceRoot + name, SHA256: entry.SHA256}, desktopEvidenceRoot+name, 8<<20)
		if !ok || int64(len(body)) != *entry.Size {
			return false
		}
	}
	return true
}
