package process

import (
	"context"
	"encoding/json"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func shutdownNodeFixture(t *testing.T) ([]string, *shutdownProof) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for cross-language fixture")
	}
	root := t.TempDir()
	module, err := filepath.Abs("../../patches/shutdown_receipt_store.mjs")
	if err != nil {
		t.Fatal(err)
	}
	writer, _ := filepath.Abs("../../patches/runtime_writer_guard.mjs")
	fileURL := func(file string) string {
		p := filepath.ToSlash(file)
		if runtime.GOOS == "windows" {
			p = "/" + p
		}
		return (&url.URL{Scheme: "file", Path: p}).String()
	}
	code := "import {createShutdownReceiptStore} from " + string(shutdownJSON(fileURL(module))) + ";import {acquireRuntimeWriter} from " + string(shutdownJSON(fileURL(writer))) + ";const directory=process.argv[1];const writer=acquireRuntimeWriter({directory,generation:7});createShutdownReceiptStore({directory,baseURL:'http://gateway.invalid/api/gateway/llm',apiKey:'test<&key',generation:7}).write(writer.claimID);"
	cmd := exec.Command(node, "--input-type=module", "-e", code, root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture failed: %v %s", err, output)
	}
	env := []string{taskProtocolFlag + "=1", renewalFlag + "=1", "CLAWMANAGER_TASK_RECEIPT_DIR=" + root, "CLAWMANAGER_LLM_BASE_URL=http://gateway.invalid/api/gateway/llm", "CLAWMANAGER_LLM_API_KEY=test<&key", "CLAWMANAGER_GATEWAY_GENERATION=7"}
	proof, err := captureShutdownProof(env)
	if err != nil {
		ownerData, ownerErr := readShutdownFile(filepath.Join(root, ".task-writer.lock", "owner.json"))
		t.Fatalf("capture failed: root=%v claim=%v owner_read=%v owner_bytes=%d; %v", shutdownDirectory(root), shutdownDirectory(filepath.Join(root, ".task-writer.lock")), ownerErr, len(ownerData), err)
	}
	return env, proof
}
func TestOpenClawShutdownAuditReadsActualNodeReceipt(t *testing.T) {
	env, p := shutdownNodeFixture(t)
	if p.verify() != "cleanup_confirmed_release_unconfirmed" {
		t.Fatal("cleanup record falsely proved release")
	}
	owner := filepath.Join(p.directory, ".task-writer.lock", "owner.json")
	if err := os.Remove(owner); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(owner)); err != nil {
		t.Fatal(err)
	}
	if p.verify() != "cleanup_confirmed_release_observed" {
		t.Fatal("cross-language signature mismatch")
	}
	m := New(appconfig.Config{})
	cmd := &exec.Cmd{Env: env}
	if m.auditStoppedProcess(cmd, p, nil, "stopped") != nil || m.Snapshot().ShutdownAudit != "cleanup_confirmed_release_observed" {
		t.Fatal("verified audit not recorded")
	}
	observed := m.Snapshot().ShutdownObservation
	if observed == nil || observed.Generation != p.generation || observed.ClaimID != p.claim || observed.Outcome != "cleanup_confirmed_release_observed" {
		t.Fatal("missing scoped shutdown observation")
	}
	observed.ClaimID = "changed by consumer"
	if m.Snapshot().ShutdownObservation.ClaimID != p.claim {
		t.Fatal("snapshot exposed mutable observation")
	}
}
func TestOpenClawRepeatedStopDoesNotClearUnconfirmedAudit(t *testing.T) {
	m := New(appconfig.Config{})
	m.shutdownAudit = "cleanup_unconfirmed"
	if m.Stop(context.Background()) == nil || m.Restart(context.Background()) == nil {
		t.Fatal("repeat stop/restart bypassed unconfirmed cleanup")
	}
	if m.Snapshot().ShutdownAudit != "cleanup_unconfirmed" {
		t.Fatal("audit was erased")
	}
}
func TestOpenClawShutdownAuditRejectsTamperingAndWrongScope(t *testing.T) {
	_, p := shutdownNodeFixture(t)
	for _, change := range []func(*shutdownProof){func(v *shutdownProof) { v.key = "other" }, func(v *shutdownProof) { v.generation++ }, func(v *shutdownProof) { v.claim = strings.Repeat("b", 64) }, func(v *shutdownProof) { v.namespace = strings.Repeat("c", 64) }} {
		v := *p
		change(&v)
		if v.verify() != "cleanup_unconfirmed" {
			t.Fatal("foreign proof accepted")
		}
	}
	file := filepath.Join(p.directory, "z-runtime-shutdowns", p.namespace+"-"+p.claim+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("invalid fixture")
	}
	value["mac"] = strings.Repeat("0", 64)
	if err := os.WriteFile(file, shutdownJSON(value), 0600); err != nil {
		t.Fatal(err)
	}
	if p.verify() != "cleanup_unconfirmed" {
		t.Fatal("invalid signature accepted")
	}
}
func TestOpenClawShutdownAuditCannotClaimSuccessWithoutProof(t *testing.T) {
	env, p := shutdownNodeFixture(t)
	m := New(appconfig.Config{})
	cmd := &exec.Cmd{Env: env}
	if m.auditStoppedProcess(cmd, p, nil, "stopped") == nil || m.Snapshot().ShutdownAudit != "cleanup_confirmed_release_unconfirmed" {
		t.Fatal("retained claim reported success")
	}
	if m.auditStoppedProcess(cmd, nil, errShutdownUnconfirmed, "killed_after_timeout") == nil || m.Snapshot().ShutdownAudit != "cleanup_unconfirmed" {
		t.Fatal("exit fabricated confirmation")
	}
	m.cmd = &exec.Cmd{}
	before := m.shutdownAudit
	if m.auditStoppedProcess(cmd, p, nil, "stopped") == nil || m.shutdownAudit != before {
		t.Fatal("old process changed new audit")
	}
	if m.auditStoppedProcess(&exec.Cmd{}, nil, nil, "stopped") != nil {
		t.Fatal("legacy behavior changed")
	}
}
func TestOpenClawShutdownAuditReadIsBoundedAndRejectsLinks(t *testing.T) {
	env, p := shutdownNodeFixture(t)
	for _, change := range []string{"CLAWMANAGER_GATEWAY_GENERATION=8", "CLAWMANAGER_LLM_API_KEY=bad\nkey", "CLAWMANAGER_TASK_RECEIPT_DIR=relative"} {
		key, _, _ := strings.Cut(change, "=")
		bad := append([]string(nil), env...)
		for i, v := range bad {
			if strings.HasPrefix(v, key+"=") {
				bad[i] = change
			}
		}
		if _, err := captureShutdownProof(bad); err == nil {
			t.Fatal("invalid capture accepted")
		}
	}
	if _, err := captureShutdownProof(append(append([]string(nil), env...), "CLAWMANAGER_GATEWAY_GENERATION=7")); err == nil {
		t.Fatal("duplicate accepted")
	}
	file := filepath.Join(p.directory, "z-runtime-shutdowns", p.namespace+"-"+p.claim+".json")
	if err := os.WriteFile(file, []byte(strings.Repeat("x", 2049)), 0600); err != nil {
		t.Fatal(err)
	}
	if p.verify() != "cleanup_unconfirmed" {
		t.Fatal("oversized evidence accepted")
	}
	if _, err := captureShutdownProof(nil); err != nil {
		t.Fatal("legacy capture changed")
	}
}
