package slots

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeUpgradeNeverSelectsBusySlot(t *testing.T) {
	invoked := false
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", RuntimeType: "openclaw", ControlToken: "control", PodUID: "pod", WorkspaceRoot: t.TempDir(), GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	p, err := NewPool(cfg, 1, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if path != "/v1/slot-state" {
			invoked = true
			return 500, nil, nil
		}
		data, _ := json.Marshal(Snapshot{Assignment: &Assignment{InstanceID: 9, Generation: 1, State: "running"}, WorkerEpoch: "0123456789abcdef0123456789abcdef"})
		return 200, data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"operation_id":"0123456789abcdef0123456789abcdef","action":"migrate","request":{"rollout_id":"r1","instance_id":2,"user_id":1,"generation":3,"uid":200002,"gid":200002,"lease_token":"parent-only"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/native-upgrades/start", strings.NewReader(body))
	request.Header.Set(gateway.ControlTokenHeader, "control")
	out := httptest.NewRecorder()
	p.ServeHTTP(out, request)
	if out.Code != 409 {
		t.Fatalf("expected busy rejection, got %d: %s", out.Code, out.Body.String())
	}
	if invoked {
		t.Fatal("busy slot received maintenance work")
	}
}

func TestNativeUncertainProofKeepsParentFences(t *testing.T) {
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", ControlToken: "control", PodUID: "pod", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	intent := containerStopIntent{Version: 1, OperationID: "0123456789abcdef0123456789abcdef", PodUID: "pod", Slot: "slot-0", GatewayID: "gw-2-3", InstanceID: 2, UserID: 1, Generation: 3, WorkerEpoch: "abcdef0123456789abcdef0123456789", AssignmentDigest: strings.Repeat("a", 64), Kind: "maintenance"}
	proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
	p, err := NewPool(cfg, 1, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if path != "/v1/native-maintenance/reconcile" {
			t.Fatalf("unexpected parent call %s", path)
		}
		raw, _ := json.Marshal(nativeMaintenanceResult{Intent: intent, Confirmed: true, Error: "NFS timeout", Uncertain: true})
		return 200, raw, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "writer-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	released := false
	session := &nativeUpgradeSession{record: &nativeUpgradeRecord{Intent: &intent, Phase: "held", Operation: gateway.NativeUpgradeOperation{Action: "migrate"}}, path: filepath.Join(t.TempDir(), "record.json"), writer: file, unlock: func() { released = true }}
	p.maintenance = map[string]*nativeUpgradeSession{intent.OperationID: session}
	if _, err = p.finishNativeMaintenance(context.Background(), proof); !errors.Is(err, gateway.ErrNativeStorageUncertain) {
		t.Fatalf("uncertain completion accepted: %v", err)
	}
	if released {
		t.Fatal("parent operation fence released")
	}
	if _, err = file.Stat(); err != nil {
		t.Fatal("parent writer lock closed")
	}
}

func TestNativeFinishedRetryReconcilesParentCleanup(t *testing.T) {
	intent := containerStopIntent{Version: 1, OperationID: strings.Repeat("a", 32), PodUID: "pod", Slot: "slot-0", Kind: "maintenance"}
	proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
	calls, unlocked := 0, false
	session := &nativeUpgradeSession{record: &nativeUpgradeRecord{Intent: &intent, Proof: &proof, Phase: "finished", Result: json.RawMessage(`{"ok":true}`)}, path: filepath.Join(t.TempDir(), "record.json"), finishLease: func() error {
		calls++
		if calls > 1 {
			t.Fatal("one-shot lease completion consumed twice")
		}
		return errors.New("renewal failed")
	}, unlock: func() { unlocked = true }}
	p := &Pool{maintenance: map[string]*nativeUpgradeSession{intent.OperationID: session}}
	if _, err := p.finishNativeMaintenance(context.Background(), proof); err == nil {
		t.Fatal("unfinished cleanup reported success")
	}
	if unlocked {
		t.Fatal("failed cleanup released operation fence")
	}
	if _, err := p.finishNativeMaintenance(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !unlocked {
		t.Fatal("exact retry leaked operation lock")
	}
}

func TestNativeCompletionFailureRemainsRetryable(t *testing.T) {
	intent := containerStopIntent{Version: 1, OperationID: strings.Repeat("a", 32), PodUID: "pod", Slot: "slot-0", Kind: "maintenance"}
	proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
	reconciles, leaseCalls, releases := 0, 0, 0
	cfg := gateway.Config{ControlToken: "control", PodUID: "pod", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	p, err := NewPool(cfg, 1, func(_ context.Context, _ int, _ string, path string, _ []byte) (int, []byte, error) {
		switch path {
		case "/v1/native-maintenance/reconcile":
			reconciles++
			data, _ := json.Marshal(nativeMaintenanceResult{Intent: intent, Confirmed: true, Result: json.RawMessage(`{"ok":true}`)})
			return 200, data, nil
		case "/v1/release-stop":
			releases++
			return 200, nil, nil
		default:
			t.Fatalf("unexpected call %s", path)
			return 500, nil, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	unlocked := false
	session := &nativeUpgradeSession{record: &nativeUpgradeRecord{Intent: &intent, Phase: "held"}, path: filepath.Join(t.TempDir(), "record.json"), finishLease: func() error {
		leaseCalls++
		if leaseCalls > 1 {
			t.Fatal("one-shot lease completion consumed twice")
		}
		return errors.New("renewal failed")
	}, unlock: func() { unlocked = true }}
	p.maintenance = map[string]*nativeUpgradeSession{intent.OperationID: session}
	if _, err = p.finishNativeMaintenance(context.Background(), proof); err == nil {
		t.Fatal("lease failure ignored")
	}
	if session.record.Phase == "finished" || unlocked {
		t.Fatal("published completion before cleanup")
	}
	if err = p.releaseNativeMaintenance(context.Background(), proof); err == nil || releases != 0 {
		t.Fatal("unfinished operation released slot")
	}
	changed := proof
	changed.ContainerID = "containerd://different"
	if _, err = p.finishNativeMaintenance(context.Background(), changed); err == nil || unlocked {
		t.Fatal("changed termination proof finalized operation")
	}
	if _, err = p.finishNativeMaintenance(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	if !unlocked || leaseCalls != 1 || reconciles != 1 {
		t.Fatalf("retry failed cleanup: unlock=%v lease=%d reconcile=%d", unlocked, leaseCalls, reconciles)
	}
	if err = p.releaseNativeMaintenance(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatal("slot not released")
	}
}

func TestNativeProvenCleanupRecoversAfterCoordinatorRestart(t *testing.T) {
	if os.Geteuid() != 10001 {
		t.Skip("requires reserved management identity")
	}
	cfg := gateway.Config{WorkspaceRoot: t.TempDir(), RuntimeType: "openclaw", PodUID: "pod", ControlToken: "control", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
	op := gateway.NativeUpgradeOperation{OperationID: strings.Repeat("b", 32), Action: "inventory", Request: gateway.WorkspaceUpgradeRequest{RolloutID: "r1", InstanceID: 2, UserID: 1, Generation: 3, UID: 200002, GID: 200002, LeaseToken: "parent-only"}}
	digest, _ := nativeOperationDigest(op)
	intent := containerStopIntent{Version: 1, OperationID: op.OperationID, PodUID: "pod", Slot: "slot-0", Kind: "maintenance", AssignmentDigest: digest}
	proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://old", FinishedAt: time.Now().UTC()}
	call := func(_ context.Context, _ int, _ string, path string, _ []byte) (int, []byte, error) {
		if path != "/v1/native-maintenance/reconcile" {
			return 200, nil, nil
		}
		raw, _ := json.Marshal(nativeMaintenanceResult{Intent: intent, Confirmed: true, Result: json.RawMessage(`{"ok":true}`)})
		return 200, raw, nil
	}
	p, err := NewPool(cfg, 1, call)
	if err != nil {
		t.Fatal(err)
	}
	path, err := p.nativeRecordPath(op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := p.metadata.LockUpgradeOperation(2)
	if err != nil {
		t.Fatal(err)
	}
	session := &nativeUpgradeSession{record: &nativeUpgradeRecord{Version: 1, Operation: op.Child(), Digest: digest, PodUID: "pod", LeaseToken: op.Request.LeaseToken, Intent: &intent, Phase: "held", Launched: true}, path: path, unlock: unlock, finishLease: func() error { return errors.New("lost renewal") }}
	p.maintenance = map[string]*nativeUpgradeSession{op.OperationID: session}
	if _, err = p.finishNativeMaintenance(context.Background(), proof); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	// Process death closes its descriptors; keep the durable record unchanged.
	if err = session.closeParent(false); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPool(cfg, 1, func(_ context.Context, _ int, _ string, path string, _ []byte) (int, []byte, error) {
		if path == "/v1/native-maintenance/reconcile" {
			t.Fatal("proven result required retired worker")
		}
		return 200, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered.maintenance = map[string]*nativeUpgradeSession{}
	resumed, err := recovered.nativeSession(context.Background(), op, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.closeParent(false)
	if lock, err := recovered.metadata.LockUpgradeOperation(2); err == nil {
		lock()
		t.Fatal("unfinished recovered cleanup lost operation fence")
	}
	if _, err = recovered.finishNativeMaintenance(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	next, err := recovered.metadata.LockUpgradeOperation(2)
	if err != nil {
		t.Fatal("recovered completion leaked operation lock:", err)
	}
	next()
	if err = recovered.releaseNativeMaintenance(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
}

func TestNativeFinalizedResumeWithoutIdleSlot(t *testing.T) {
	if os.Geteuid() != 10001 {
		t.Skip("requires reserved management identity")
	}
	for _, phase := range []string{"finished", "released", "uncertain", "missing", "changed-intent"} {
		t.Run(phase, func(t *testing.T) {
			cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", WorkspaceRoot: t.TempDir(), RuntimeType: "openclaw", PodUID: "pod", ControlToken: "control", GatewayPortStart: 20000, GatewayPortEnd: 20002, GatewayPortBlockSize: 3}
			op := gateway.NativeUpgradeOperation{OperationID: strings.Repeat("c", 32), Action: "migrate", Request: gateway.WorkspaceUpgradeRequest{RolloutID: "r1", InstanceID: 2, UserID: 1, Generation: 3, UID: 200002, GID: 200002, LeaseToken: "parent-only"}}
			digest, _ := nativeOperationDigest(op)
			intent := containerStopIntent{Version: 1, OperationID: op.OperationID, PodUID: cfg.PodUID, Slot: "slot-0", GatewayID: "gw-2-3", InstanceID: 2, UserID: 1, Generation: 3, WorkerEpoch: strings.Repeat("e", 32), AssignmentDigest: digest, Kind: "maintenance"}
			proof := containerTerminationProof{containerTerminationMessage: containerTerminationMessage{containerStopIntent: intent, CleanupConfirmed: true}, ContainerID: "containerd://retired", FinishedAt: time.Now().UTC()}
			releases := 0
			newPool := func() *Pool {
				p, err := NewPool(cfg, 1, func(_ context.Context, _ int, _ string, path string, _ []byte) (int, []byte, error) {
					if path != "/v1/release-stop" {
						t.Fatalf("durable resume accessed occupied worker: %s", path)
					}
					releases++
					return 200, nil, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			original := newPool()
			path, err := original.nativeRecordPath(op.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			record := nativeUpgradeRecord{Version: 1, Operation: op.Child(), LeaseToken: op.Request.LeaseToken, Digest: digest, PodUID: cfg.PodUID, Slot: 0, Phase: phase, Launched: true, Intent: &intent, Proof: &proof, Result: json.RawMessage(`{"ok":true}`)}
			if phase == "changed-intent" {
				record.Phase = "finished"
				changed := intent
				changed.WorkerEpoch = strings.Repeat("d", 32)
				record.Intent = &changed
			}
			if phase != "missing" {
				if err = atomicJSON(path, record); err != nil {
					t.Fatal(err)
				}
			}
			// Fresh coordinator has no sessions, and no worker state is available:
			// every slot may already belong to a newer running assignment.
			p := newPool()
			post := func(path string, value any) *httptest.ResponseRecorder {
				body, _ := json.Marshal(value)
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				r.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
				out := httptest.NewRecorder()
				p.ServeHTTP(out, r)
				return out
			}
			out := post("/v1/native-upgrades/resume", op)
			if phase != "finished" && phase != "released" {
				if out.Code != 409 {
					t.Fatalf("invalid durable replay accepted: %d %s", out.Code, out.Body)
				}
				if releases != 0 {
					t.Fatal("invalid durable record released worker")
				}
				return
			}
			if out.Code != 202 {
				t.Fatalf("no-idle durable replay failed: %d %s", out.Code, out.Body)
			}
			var pending nativeMaintenancePending
			if json.Unmarshal(out.Body.Bytes(), &pending) != nil || !pending.Pending || pending.Intent != intent {
				t.Fatal("resumed execution binding changed")
			}
			changed := proof
			changed.ContainerID = "containerd://different"
			if out = post("/v1/native-upgrades/"+op.OperationID+"/release", changed); out.Code != 409 {
				t.Fatal("changed old-container proof accepted")
			}
			if out = post("/v1/native-upgrades/"+op.OperationID+"/finish", proof); out.Code != 200 {
				t.Fatalf("saved result unavailable: %d %s", out.Code, out.Body)
			}
			for i := 0; i < 2; i++ {
				if out = post("/v1/native-upgrades/"+op.OperationID+"/release", proof); out.Code != 200 {
					t.Fatalf("release failed: %d %s", out.Code, out.Body)
				}
			}
			want := 1
			if phase == "released" {
				want = 0
			}
			if releases != want {
				t.Fatalf("retired worker replayed: releases=%d want=%d", releases, want)
			}
		})
	}
}
