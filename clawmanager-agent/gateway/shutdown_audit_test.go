package gateway

import (
	"context"
	"encoding/json"
	"errors"
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
	// Go's t.TempDir subdirectory uses the process umask. Match the private
	// receipt directory required by the production writer on Unix.
	root := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../../openclaw/patches/shutdown_receipt_store.mjs")
	if err != nil {
		t.Fatal(err)
	}
	writer, _ := filepath.Abs("../../openclaw/patches/runtime_writer_guard.mjs")
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

func liteShutdownFixture(t *testing.T, stop func(context.Context) error) (*GatewayManager, ConfirmStopRequest, *shutdownProof) {
	env, proof := shutdownNodeFixture(t)
	m, req := stopFixture(t, func(context.Context) error { return nil })
	m.cfg.RuntimeType = "openclaw"
	r := m.gateways[req.GatewayID]
	r.startSpec = GatewayStartSpec{Env: env}
	r.process.Stop = stop
	return m, req, proof
}
func TestOpenClawLiteShutdownRetainsBindingUntilSignedRelease(t *testing.T) {
	m, req, proof := liteShutdownFixture(t, func(context.Context) error { return nil })
	if m.StopGatewayConfirmed(context.Background(), req.GatewayID) == nil {
		t.Fatal("retained writer reported successful stop")
	}
	record := m.gateways[req.GatewayID]
	if record == nil || record.state.ShutdownAudit != "cleanup_confirmed_release_unconfirmed" || !record.deletePending {
		t.Fatal("lost failure audit/fence")
	}
	observation := record.state.ShutdownObservation
	if observation == nil || observation.Generation != proof.generation || observation.ClaimID != proof.claim || observation.Outcome != record.state.ShutdownAudit {
		t.Fatal("missing scoped Lite observation")
	}
	states := m.GatewayStates()
	if len(states) != 1 || states[0].ShutdownObservation == nil {
		t.Fatal("report lost observation")
	}
	states[0].ShutdownObservation.ClaimID = "changed by consumer"
	if record.state.ShutdownObservation.ClaimID != proof.claim {
		t.Fatal("snapshot exposed mutable observation")
	}
	if _, err := m.ports.Reserve(99, 1, PortRange{Start: 31000, End: 31000}); err == nil {
		t.Fatal("port released before confirmation")
	}
	owner := filepath.Join(proof.directory, ".task-writer.lock", "owner.json")
	if err := os.Remove(owner); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(owner)); err != nil {
		t.Fatal(err)
	}
	if err := m.StopGatewayConfirmed(context.Background(), req.GatewayID); err != nil {
		t.Fatal(err)
	}
	if m.gateways[req.GatewayID] != nil {
		t.Fatal("verified binding not removed")
	}
	if _, err := m.ports.Reserve(99, 1, PortRange{Start: 31000, End: 31000}); err != nil {
		t.Fatal("verified port not released", err)
	}
}
func TestOpenClawLiteStoppedWatcherCannotBypassFailedStopAudit(t *testing.T) {
	var m *GatewayManager
	var req ConfirmStopRequest
	m, req, _ = liteShutdownFixture(t, func(context.Context) error {
		m.mu.Lock()
		m.gateways[req.GatewayID].state.State = "stopped"
		m.mu.Unlock()
		return errShutdownUnconfirmed
	})
	if m.StopGatewayConfirmed(context.Background(), req.GatewayID) == nil {
		t.Fatal("stopped watcher bypassed audit")
	}
	r := m.gateways[req.GatewayID]
	if r == nil || r.state.ShutdownAudit != "cleanup_unconfirmed" || r.deleteBusy {
		t.Fatal("fence lost or retry blocked")
	}
}
func TestOpenClawLiteTamperedReceiptRetainsOriginalClaim(t *testing.T) {
	m, req, proof := liteShutdownFixture(t, func(context.Context) error { return nil })
	file := filepath.Join(proof.directory, "z-runtime-shutdowns", proof.namespace+"-"+proof.claim+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		t.Fatal("fixture invalid")
	}
	payload["mac"] = strings.Repeat("0", 64)
	if err := os.WriteFile(file, shutdownJSON(payload), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if m.StopGatewayConfirmed(context.Background(), req.GatewayID) == nil {
			t.Fatal("tampering accepted")
		}
	}
	if m.gateways[req.GatewayID].state.ShutdownAudit != "cleanup_unconfirmed" {
		t.Fatal("wrong audit")
	}
	if _, err := os.Stat(filepath.Join(proof.directory, ".task-writer.lock")); err != nil {
		t.Fatal("claim removed")
	}
}
func TestOpenClawLiteLegacyStopDoesNotRequireReceipt(t *testing.T) {
	m, req := stopFixture(t, func(context.Context) error { return nil })
	m.cfg.RuntimeType = "openclaw"
	m.gateways[req.GatewayID].process.Stop = func(context.Context) error { return nil }
	if err := m.StopGatewayConfirmed(context.Background(), req.GatewayID); err != nil {
		t.Fatal(err)
	}
}
func TestOpenClawLiteUnexpectedExitRetainsReservationAndIdentity(t *testing.T) {
	m, req, _ := liteShutdownFixture(t, func(context.Context) error { return nil })
	done := make(chan error, 1)
	done <- errors.New("process disappeared")
	close(done)
	record := m.gateways[req.GatewayID]
	record.process.Done = done
	m.watchGatewayProcess(req.GatewayID, record.process.PID, done)
	if m.gateways[req.GatewayID] != record || record.process.PID != 1234 || !record.deletePending || record.state.ShutdownAudit != "cleanup_unconfirmed" || record.restartAttempts != 0 {
		t.Fatal("unexpected exit lost fence or scheduled replacement")
	}
	if _, err := m.ports.Reserve(99, 1, PortRange{Start: 31000, End: 31000}); err == nil {
		t.Fatal("unexpected exit released port")
	}
}
