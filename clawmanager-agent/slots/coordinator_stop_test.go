package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestCoordinatorFinalizesWithoutReplacementWorker(t *testing.T) {
	for _, variant := range []string{"openclaw", "hermes", "opencode", "deepseek-harness", "force-delete"} {
		t.Run(variant, func(t *testing.T) {
			runtimeType := variant
			if variant == "force-delete" {
				runtimeType = "openclaw"
			}
			stateRoot, sockets := t.TempDir(), t.TempDir()
			t.Setenv("RUNTIME_COORDINATOR_SLOT_STATE_ROOT", stateRoot)
			t.Setenv("RUNTIME_SLOT_SOCKET_DIR", sockets)
			cfg := gateway.Config{ControlToken: "secret", SandboxProfile: "nfs-rootless-v1", RuntimeType: runtimeType, PodUID: "pod", WorkspaceRoot: t.TempDir(), AgentDataDir: filepath.Join(stateRoot, "slot-0"), GatewayPortStart: 20000, GatewayPortEnd: 20000, GatewayPortBlockSize: 1}
			b, err := openBook(cfg.AgentDataDir)
			if err != nil {
				t.Fatal(err)
			}
			req := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 2, Generation: 1, AgentType: runtimeType, PodUID: cfg.PodUID}
			if err = b.begin(req, strings.Repeat("1", 32)); err != nil {
				t.Fatal(err)
			}
			intent := containerStopIntent{Version: 1, OperationID: strings.Repeat("2", 32), PodUID: cfg.PodUID, Slot: "slot-0", GatewayID: "gw-7-1", InstanceID: 7, UserID: 2, Generation: 1, WorkerEpoch: b.Assignment.WorkerEpoch, AssignmentDigest: b.Digest}
			if variant == "force-delete" {
				intent.Kind = variant
			}
			b.StopIntent = &intent
			if err = b.save(); err != nil {
				t.Fatal(err)
			}
			worker := &Worker{cfg: cfg, book: b, slotName: intent.Slot}
			writerLock, writerPath, err := worker.writerPaths(req)
			if err != nil {
				t.Fatal(err)
			}
			if err = claimWriter(writerPath, req, intent.Slot); err != nil {
				t.Fatal(err)
			}
			calls := 0
			p, err := NewPool(cfg, 1, func(context.Context, int, string, string, []byte) (int, []byte, error) {
				calls++
				return 0, nil, errors.New("replacement unavailable for 300s")
			})
			if err != nil {
				t.Fatal(err)
			}
			proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
			call := func(action string, value containerTerminationProof) *httptest.ResponseRecorder {
				data, _ := json.Marshal(value)
				r := httptest.NewRequest(http.MethodPost, "/v1/slots/slot-0/"+action, bytes.NewReader(data))
				r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
				out := httptest.NewRecorder()
				p.ServeHTTP(out, r)
				return out
			}
			// Reuse the production supervisor's existing lifetime ownership lock.
			guard, err := os.OpenFile(filepath.Join(sockets, "slot-0.sock.guard"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			if err = lockWorkspace(guard); err != nil {
				t.Fatal(err)
			}
			if out := call("reconcile-stop", proof); out.Code == 200 {
				t.Fatal("coordinator wrote a live worker's book")
			}
			if err = guard.Close(); err != nil {
				t.Fatal(err)
			}
			stale := proof
			stale.OperationID = strings.Repeat("3", 32)
			if out := call("reconcile-stop", stale); out.Code != 409 {
				t.Fatalf("stale proof: %d %s", out.Code, out.Body.String())
			}
			lease, err := os.OpenFile(writerLock, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err = lockWorkspace(lease); err != nil {
				t.Fatal(err)
			}
			if out := call("reconcile-stop", proof); out.Code != 409 {
				t.Fatalf("live workspace writer: %d", out.Code)
			}
			lease.Close()
			if out := call("reconcile-stop", proof); out.Code != 200 {
				t.Fatalf("offline reconcile: %d %s", out.Code, out.Body.String())
			}
			b, err = openBook(cfg.AgentDataDir)
			if err != nil || !b.quarantined || b.Assignment.State != "stopped" {
				t.Fatalf("lost pending release: %+v %v", b, err)
			}
			if out := call("reconcile-stop", proof); out.Code != 200 {
				t.Fatalf("reconcile retry: %d", out.Code)
			}
			if out := call("release-stop", stale); out.Code != 409 {
				t.Fatalf("stale release: %d", out.Code)
			}
			changed := proof
			changed.ContainerID = "containerd://different"
			if out := call("reconcile-stop", changed); out.Code != 409 {
				t.Fatalf("changed container proof: %d", out.Code)
			}
			changed = proof
			changed.FinishedAt = proof.FinishedAt.Add(time.Second)
			if out := call("release-stop", changed); out.Code != 409 {
				t.Fatalf("changed termination time: %d", out.Code)
			}
			b.Maintenance = &nativeMaintenanceBook{Uncertain: true}
			if err = b.save(); err != nil {
				t.Fatal(err)
			}
			if out := call("release-stop", proof); out.Code != 409 {
				t.Fatalf("uncertain storage released: %d", out.Code)
			}
			b.Maintenance = nil
			if err = b.save(); err != nil {
				t.Fatal(err)
			}
			if out := call("release-stop", proof); out.Code != 200 {
				t.Fatalf("offline release: %d %s", out.Code, out.Body.String())
			}
			b, err = openBook(cfg.AgentDataDir)
			if err != nil || b.quarantined || !b.StopFinalized {
				t.Fatalf("slot not reusable: %+v %v", b, err)
			}
			next := req
			next.InstanceID = 8
			if err = b.begin(next, strings.Repeat("4", 32)); err != nil {
				t.Fatal(err)
			}
			if out := call("release-stop", proof); out.Code != 200 {
				t.Fatalf("release ack after reuse: %d", out.Code)
			}
			b, err = openBook(cfg.AgentDataDir)
			if err != nil || b.Assignment.InstanceID != 8 || b.Assignment.State != "starting" {
				t.Fatal("old release changed newer assignment")
			}
			if calls != 1 {
				t.Fatalf("offline finalization called unavailable worker %d times", calls)
			}
		})
	}
}
