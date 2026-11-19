//go:build linux

package hermes

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in integration check, executed inside the built Lite image. Unlike unit
// fixtures this exercises the real release assembler/Go verifier contract.
func TestDesktopCapabilityActualImage(t *testing.T) {
	if os.Getenv("HERMES_TEST_ACTUAL_IMAGE") != "1" {
		t.Skip("requires built Lite image")
	}
	cfg := desktopCapabilityConfig(t)
	cfg.GatewayCommand = NewProfile("hermes").GatewayCommand("")
	if got := NewProfile("hermes").withVerifiedCapabilities(cfg, "/").HealthCapabilities(); got == nil || got.HermesDesktopWeb == nil {
		t.Fatal("built Lite image did not expose verified Web capability")
	}
}

// These reports/artifacts are synthetic unit fixtures, never production evidence.
func desktopAcceptedReleaseFixture(t *testing.T) (string, desktopRelease) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("release trust uses image root ownership")
	}
	root := t.TempDir()
	r := desktopRelease{SchemaVersion: 2, HermesRef: desktopWebHermesGitRef, HermesCommit: desktopWebHermesCommit, PackageVersion: desktopWebHermesVersion, NodeVersion: "22.23.2", ContractVersion: 1, RPCProtocol: "hermes-jsonrpc-v1", BackendMode: "serve", AuthMode: "password-cookie", Accepted: true, Artifacts: map[string]string{}}
	files := append([]string(nil), desktopRequiredArtifacts...)
	for _, script := range desktopAcceptanceScripts {
		files = append(files, "/usr/local/share/hermes-lite/tests/"+script)
	}
	for _, file := range files {
		body := []byte("synthetic test fixture: " + file)
		switch file {
		case desktopShareRoot + "release-trust.json":
			body, _ = json.Marshal(desktopCampaignTrust{SchemaVersion: 1, Algorithm: "Ed25519", KeyID: "unit-test-only", PublicKeyBase64: base64.StdEncoding.EncodeToString(desktopFixtureKey().Public().(ed25519.PublicKey)), KeyOrigin: "local-operator"})
		case desktopShareRoot + "acceptance-protocol.json":
			body, _ = json.Marshal(desktopAcceptanceProtocol{SchemaVersion: 1, Protocol: desktopCampaignProtocol, ReportSchemaVersion: 2, Suites: desktopCampaignSuites, PromotionSuites: desktopCampaignSuites[:5], ObservationChecks: desktopObservationChecks, BrowserChecks: desktopBrowserChecks})
		}
		writeDesktopFixture(t, root, file, body)
		r.Artifacts[file] = desktopDigest(body)
	}
	var paths []string
	for path := range r.Artifacts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var payload strings.Builder
	for _, path := range paths {
		payload.WriteString(path + "\x00" + r.Artifacts[path] + "\n")
	}
	r.PayloadSHA256 = desktopDigest([]byte(payload.String()))
	writeDesktopCampaignFixture(t, root, &r)
	writeDesktopReleaseFixture(t, root, r)
	return root, r
}

func writeDesktopFixture(t *testing.T, root, path string, body []byte) {
	t.Helper()
	full := filepath.Join(root, strings.TrimPrefix(path, "/"))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0755); err != nil {
		t.Fatal(err)
	}
}
func writeDesktopReleaseFixture(t *testing.T, root string, r desktopRelease) {
	t.Helper()
	body, _ := json.Marshal(r)
	writeDesktopFixture(t, root, desktopReleasePath, body)
}

func TestDesktopCapabilityAcceptedImmutableSnapshotWithoutGateways(t *testing.T) {
	root, release := desktopAcceptedReleaseFixture(t)
	cfg := desktopCapabilityConfig(t)
	p := NewProfile("hermes").withVerifiedCapabilities(cfg, root)
	if c := p.HealthCapabilities(); c == nil || !c.HermesDesktopWeb.Enabled || c.HermesDesktopWeb.ContractVersion != 2 || c.HermesDesktopWeb.HermesCommit != desktopWebHermesCommit || !c.HermesDesktopWeb.ArtifactsVerified || !c.HermesDesktopWeb.ReleaseAccepted || c.HermesDesktopWeb.PayloadSHA256 != release.PayloadSHA256 {
		t.Fatal("verified fixture omitted capability")
	}
	_ = os.Remove(filepath.Join(root, desktopReleasePath))
	t.Setenv(desktopWebFlag, "false")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := p.HealthCapabilities()
			if c == nil || !c.HermesDesktopWeb.Enabled {
				t.Error("snapshot changed after initialization")
			}
			c.HermesDesktopWeb.Enabled = false
		}()
	}
	wg.Wait()
	if _, err := os.Stat(cfg.WorkspaceRoot); !os.IsNotExist(err) {
		t.Fatal("capability created user workspace")
	}
}

func TestDesktopCapabilityVerifiedCandidateDoesNotClaimSignedAcceptance(t *testing.T) {
	root, release := desktopAcceptedReleaseFixture(t)
	release.Accepted = false
	release.Acceptance = nil
	writeDesktopReleaseFixture(t, root, release)
	// Compatibility does not depend on unsigned or stale reports in the image.
	if err := os.Remove(filepath.Join(root, "usr/local/share/hermes-lite/evidence/campaign-signature.json")); err != nil {
		t.Fatal(err)
	}
	cfg := desktopCapabilityConfig(t)
	// Exercise the actual profile command used by deployed Web + Team images.
	cfg.GatewayCommand = NewProfile("hermes").GatewayCommand("")
	capabilities := NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities()
	if capabilities == nil {
		t.Fatal("fully verified candidate omitted protocol compatibility")
	}
	c := capabilities.HermesDesktopWeb
	if c.ContractVersion != 2 || !c.Enabled || !c.ArtifactsVerified || c.ReleaseAccepted || c.PayloadSHA256 != release.PayloadSHA256 {
		t.Fatalf("candidate compatibility confused with signed acceptance: %+v", c)
	}
}

func TestDesktopCapabilityCandidateStillRequiresActualTrustedArtifacts(t *testing.T) {
	for _, name := range []string{"modified", "writable", "symlink", "nonroot", "nonexecutable", "missing"} {
		t.Run(name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			release.Accepted, release.Acceptance = false, nil
			writeDesktopReleaseFixture(t, root, release)
			file := filepath.Join(root, "usr/local/bin/start-hermes-lite-backend")
			var err error
			switch name {
			case "modified":
				err = os.WriteFile(file, []byte("changed"), 0755)
			case "writable":
				err = os.Chmod(file, 0777)
			case "symlink":
				if err = os.Rename(file, file+".real"); err == nil {
					err = os.Symlink(file+".real", file)
				}
			case "nonroot":
				err = os.Chown(file, 200001, 200001)
			case "nonexecutable":
				err = os.Chmod(file, 0644)
			case "missing":
				err = os.Remove(file)
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("untrusted candidate claimed compatibility")
			}
		})
	}
}

func TestDesktopCapabilityCandidateRequiresExplicitAcceptanceState(t *testing.T) {
	for _, value := range []string{"missing", "null", "string", "number"} {
		t.Run(value, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			release.Accepted, release.Acceptance = false, nil
			body, _ := json.Marshal(release)
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			switch value {
			case "missing":
				delete(fields, "desktop_web_accepted")
			case "null":
				fields["desktop_web_accepted"] = nil
			case "string":
				fields["desktop_web_accepted"] = "false"
			case "number":
				fields["desktop_web_accepted"] = 0
			}
			body, _ = json.Marshal(fields)
			writeDesktopFixture(t, root, desktopReleasePath, body)
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("missing or invalid acceptance state claimed compatibility")
			}
		})
	}
}

func TestDesktopCapabilityRejectsInconsistentAcceptanceOrInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*desktopRelease)
	}{
		{"acceptance false", func(r *desktopRelease) { r.Accepted = false }},
		{"boolean alone", func(r *desktopRelease) { r.Acceptance = nil }},
		{"old schema", func(r *desktopRelease) { r.SchemaVersion = 1 }},
		{"wrong ref", func(r *desktopRelease) { r.HermesRef = "latest" }},
		{"wrong commit", func(r *desktopRelease) { r.HermesCommit = strings.Repeat("a", 40) }},
		{"unknown contract", func(r *desktopRelease) { r.ContractVersion = 2 }},
		{"wrong backend", func(r *desktopRelease) { r.BackendMode = "dashboard" }},
		{"payload mismatch", func(r *desktopRelease) { r.PayloadSHA256 = strings.Repeat("0", 64) }},
		{"missing CM evidence", func(r *desktopRelease) { delete(r.Acceptance.Reports, "cm_bff_browser") }},
		{"missing non-native evidence", func(r *desktopRelease) { delete(r.Acceptance.Reports, "lite_non_native") }},
		{"extra suite", func(r *desktopRelease) { r.Acceptance.Reports["unknown"] = r.Acceptance.Reports["desktop_rpc"] }},
		{"foreign candidate", func(r *desktopRelease) { r.Acceptance.CandidateImageDigest = "sha256:" + strings.Repeat("c", 64) }},
		{"report hash mismatch", func(r *desktopRelease) {
			x := r.Acceptance.Reports["desktop_rpc"]
			x.SHA256 = strings.Repeat("0", 64)
			r.Acceptance.Reports["desktop_rpc"] = x
		}},
		{"report path escape", func(r *desktopRelease) {
			x := r.Acceptance.Reports["desktop_rpc"]
			x.Path = "/workspaces/private.json"
			r.Acceptance.Reports["desktop_rpc"] = x
		}},
		{"missing launcher", func(r *desktopRelease) { delete(r.Artifacts, "/usr/local/bin/start-hermes-lite-backend") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, r := desktopAcceptedReleaseFixture(t)
			tc.edit(&r)
			writeDesktopReleaseFixture(t, root, r)
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("invalid release claimed enabled")
			}
		})
	}
}

func TestDesktopCapabilityRejectsChangedOrUntrustedArtifacts(t *testing.T) {
	for _, name := range []string{"modified", "writable", "symlink", "nonroot", "nonexecutable"} {
		t.Run(name, func(t *testing.T) {
			root, _ := desktopAcceptedReleaseFixture(t)
			file := filepath.Join(root, "usr/local/bin/start-hermes-lite-backend")
			switch name {
			case "modified":
				_ = os.WriteFile(file, []byte("changed"), 0755)
			case "writable":
				_ = os.Chmod(file, 0777)
			case "symlink":
				_ = os.Rename(file, file+".real")
				_ = os.Symlink(file+".real", file)
			case "nonroot":
				_ = os.Chown(file, 200001, 200001)
			case "nonexecutable":
				_ = os.Chmod(file, 0644)
			}
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("untrusted artifact enabled capability")
			}
		})
	}
}

func TestDesktopCapabilityRejectsInvalidReportEvenWithMatchingFileHash(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*desktopAcceptanceReport)
	}{
		{"failed", func(r *desktopAcceptanceReport) { r.Status = "failed" }},
		{"missing exit code", func(r *desktopAcceptanceReport) { r.ExitCode = nil }},
		{"different payload", func(r *desktopAcceptanceReport) { r.PayloadSHA256 = strings.Repeat("c", 64) }},
		{"different script", func(r *desktopAcceptanceReport) {
			r.ScriptPath = "/usr/local/share/hermes-lite/tests/smoke_lite_image.py"
		}},
		{"different runner", func(r *desktopAcceptanceReport) { r.RunnerSHA256 = strings.Repeat("d", 64) }},
		{"missing output hash", func(r *desktopAcceptanceReport) { r.OutputSHA256 = "" }},
		{"foreign output path", func(r *desktopAcceptanceReport) { r.OutputPath = "/workspaces/result.log" }},
		{"modified output hash", func(r *desktopAcceptanceReport) { r.OutputSHA256 = strings.Repeat("e", 64) }},
		{"reversed dates", func(r *desktopAcceptanceReport) { r.StartedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, release := desktopAcceptedReleaseFixture(t)
			ref := release.Acceptance.Reports["desktop_rpc"]
			body, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(ref.Path, "/")))
			if err != nil {
				t.Fatal(err)
			}
			var report desktopAcceptanceReport
			if json.Unmarshal(body, &report) != nil {
				t.Fatal("invalid fixture report")
			}
			tc.edit(&report)
			body, _ = json.Marshal(report)
			writeDesktopFixture(t, root, ref.Path, body)
			ref.SHA256 = desktopDigest(body)
			release.Acceptance.Reports["desktop_rpc"] = ref
			writeDesktopReleaseFixture(t, root, release)
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("invalid report with updated checksum claimed capability")
			}
		})
	}
}

func TestDesktopCapabilityRejectsUntrustedOutput(t *testing.T) {
	for _, name := range []string{"missing", "modified", "symlink", "writable", "nonroot"} {
		t.Run(name, func(t *testing.T) {
			root, _ := desktopAcceptedReleaseFixture(t)
			file := filepath.Join(root, "usr/local/share/hermes-lite/evidence/desktop_rpc.log")
			switch name {
			case "missing":
				_ = os.Remove(file)
			case "modified":
				_ = os.WriteFile(file, []byte("forged success"), 0755)
			case "symlink":
				_ = os.Rename(file, file+".real")
				_ = os.Symlink(file+".real", file)
			case "writable":
				_ = os.Chmod(file, 0777)
			case "nonroot":
				_ = os.Chown(file, 200001, 200001)
			}
			cfg := desktopCapabilityConfig(t)
			if NewProfile("hermes").withVerifiedCapabilities(cfg, root).HealthCapabilities() != nil {
				t.Fatal("untrusted output enabled capability")
			}
		})
	}
}
