package slots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestNativeMaintenanceReservesOnlyExplicitIdleSlot(t *testing.T) {
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", PodUID: "pod", AgentDataDir: t.TempDir()}
	b, err := openBook(cfg.AgentDataDir)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: cfg, book: b, slotName: "slot-1", workerEpoch: strings.Repeat("1", 32), network: &networkOwner{}, manager: gateway.NewGatewayManager(cfg, nil, nil)}
	op := json.RawMessage(`{"operation_id":"22222222222222222222222222222222","action":"inventory","request":{"instance_id":7,"user_id":2,"generation":3}}`)
	intent, err := w.reserveNativeMaintenance(strings.Repeat("2", 32), op, 7, 2, 3)
	if err != nil || intent.Kind != "maintenance" || intent.Slot != "slot-1" || intent.WorkerEpoch != w.workerEpoch {
		t.Fatalf("dedicated maintenance reservation missing: %+v %v", intent, err)
	}
	reopened, err := openBook(cfg.AgentDataDir)
	if err != nil || !reopened.quarantined || reopened.Maintenance == nil || reopened.Maintenance.OperationID != intent.OperationID {
		t.Fatal("restart lost maintenance operation fence")
	}
	if _, err = w.reserveNativeMaintenance(strings.Repeat("3", 32), op, 8, 2, 1); err == nil {
		t.Fatal("busy maintenance slot admitted a second operation")
	}
	if _, err = w.reserveNativeMaintenance(strings.Repeat("2", 32), op, 7, 2, 3); err != nil {
		t.Fatal("exact operation retry was not idempotent")
	}
}

func TestNativeMaintenanceResultRequiresOldContainerProof(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", PodUID: "pod", AgentDataDir: t.TempDir(), WorkspaceRoot: t.TempDir()}
			b, err := openBook(cfg.AgentDataDir)
			if err != nil {
				t.Fatal(err)
			}
			w := &Worker{cfg: cfg, book: b, slotName: "slot-1", workerEpoch: strings.Repeat("1", 32), network: &networkOwner{}, manager: gateway.NewGatewayManager(cfg, nil, nil)}
			op := json.RawMessage(`{"operation_id":"22222222222222222222222222222222","action":"inventory","request":{"instance_id":7,"user_id":2,"generation":3}}`)
			intent, err := w.reserveNativeMaintenance(strings.Repeat("2", 32), op, 7, 2, 3)
			if err != nil {
				t.Fatal(err)
			}
			w.book.Maintenance.Ready = true
			w.book.Maintenance.Uncertain = uncertain
			w.book.Maintenance.Result = json.RawMessage(`{"status":"complete"}`)
			if err = w.book.save(); err != nil {
				t.Fatal(err)
			}
			proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
			data, _ := json.Marshal(proof)
			call := func() *httptest.ResponseRecorder {
				out := httptest.NewRecorder()
				w.serveNativeMaintenanceReconcile(out, httptest.NewRequest(http.MethodPost, "/v1/native-maintenance/reconcile", bytes.NewReader(data)))
				return out
			}
			if call().Code != 409 {
				t.Fatal("live old container published a native result")
			}
			w.book, err = openBook(cfg.AgentDataDir)
			if err != nil {
				t.Fatal(err)
			}
			w.workerEpoch = strings.Repeat("3", 32)
			writerPath := filepath.Join(cfg.WorkspaceRoot, "parent-writer.json")
			original := []byte(`{"state":"upgrading"}`)
			if err = os.WriteFile(writerPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			out := call()
			if out.Code != 200 {
				t.Fatalf("maintenance proof unavailable: %s", out.Body.String())
			}
			var result nativeMaintenanceResult
			if json.Unmarshal(out.Body.Bytes(), &result) != nil || !result.Confirmed || result.Uncertain != uncertain {
				t.Fatal("result lost its cleanup/uncertainty identity")
			}
			after, _ := os.ReadFile(writerPath)
			if !bytes.Equal(after, original) {
				t.Fatal("worker released parent NFS writer")
			}
			out = httptest.NewRecorder()
			w.serveContainerRelease(out, httptest.NewRequest(http.MethodPost, "/v1/release-stop", bytes.NewReader(data)))
			if uncertain && (out.Code != 409 || !w.book.quarantined) {
				t.Fatal("uncertain native storage allowed slot reuse")
			}
			if !uncertain && (out.Code != 200 || w.book.quarantined) {
				t.Fatal("confirmed native operation did not finalize")
			}
		})
	}
}
