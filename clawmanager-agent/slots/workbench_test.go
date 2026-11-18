package slots

import (
	"context"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workbench"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPoolWorkbenchRequiresUniqueActiveAssignment(t *testing.T) {
	for _, mode := range []string{"valid", "stale", "duplicate", "quarantined", "stopping", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			cfg := gateway.Config{ControlToken: "secret", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}
			p, _ := NewPool(cfg, 2, func(_ context.Context, index int, method, path string, body []byte) (int, []byte, error) {
				if path == "/v1/slot-state" {
					snapshot := Snapshot{}
					if index == 1 || mode == "duplicate" {
						snapshot.Assignment = &Assignment{InstanceID: 1, UserID: 2, Generation: 3, GatewayID: "gw-1-3", State: "starting"}
						snapshot.Gateways = []gateway.GatewayState{{InstanceID: 1, UserID: 2, Generation: 3, GatewayID: "gw-1-3", State: "running"}}
						snapshot.Quarantined = mode == "quarantined"
						if mode == "stopping" {
							snapshot.Assignment.State = "stopping"
						}
						if mode == "unknown" {
							snapshot.Assignment = nil
						}
						if mode == "stale" {
							snapshot.Assignment.Generation = 4
						}
					}
					b, _ := json.Marshal(snapshot)
					return 200, b, nil
				}
				calls++
				if index != 1 {
					t.Error("wrong slot")
				}
				return 200, []byte(`{"git":true}`), nil
			})
			r := httptest.NewRequest("POST", "/v1/workbench", strings.NewReader(`{"instance_id":1,"user_id":2,"generation":3,"gateway_id":"gw-1-3","operation":"capabilities","data":{}}`))
			r.Header.Set(gateway.ControlTokenHeader, "secret")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if mode == "valid" {
				if w.Code != 200 || calls != 1 {
					t.Fatalf("%d %d %s", w.Code, calls, w.Body.String())
				}
			} else if w.Code != 409 || calls != 0 {
				t.Fatalf("unsafe routing: %d %d %s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestWorkbenchLargeJSONOutputUsesTrustedLimit(t *testing.T) {
	var capture boundedCommandOutput
	capture.stdout.limit = 2 << 20
	payload := strings.Repeat("a", 100<<10)
	capture.Stdout().Write([]byte(payload))
	got, err := capture.Result(nil)
	if err != nil || string(got) != payload {
		t.Fatalf("workbench JSON truncated: %d %v", len(got), err)
	}
	var old boundedCommandOutput
	old.Stdout().Write([]byte(payload))
	if _, err = old.Result(nil); err == nil {
		t.Fatal("old callers lost bounded default")
	}
}

func TestWorkbenchRoutesPastUnrelatedOfflineSlotWithExclusiveNFSWriter(t *testing.T) {
	const epoch = "0123456789abcdef0123456789abcdef"
	for _, mode := range []string{"owned", "hung_neighbor", "no_lease", "old_epoch", "other_pod", "duplicate", "quarantined", "wrong_profile"} {
		t.Run(mode, func(t *testing.T) {
			cfg := gateway.Config{ControlToken: "secret", PodUID: "pod", SandboxProfile: "nfs-rootless-v1", GatewayPortStart: 20000, GatewayPortEnd: 20001, GatewayPortBlockSize: 1}
			if mode == "wrong_profile" {
				cfg.SandboxProfile = ""
			}
			p, err := NewPool(cfg, 2, func(ctx context.Context, index int, method, path string, data []byte) (int, []byte, error) {
				if path != "/v1/slot-state" {
					t.Fatalf("unexpected route %s", path)
				}
				if index == 0 && mode == "hung_neighbor" {
					<-ctx.Done()
				}
				if index == 0 && mode != "duplicate" {
					return 503, nil, nil
				}
				a := Assignment{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: "gw-7-2", State: "starting", WorkerEpoch: epoch, PodUID: "pod"}
				if mode == "old_epoch" {
					a.WorkerEpoch = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				}
				if mode == "other_pod" {
					a.PodUID = "different"
				}
				raw, _ := json.Marshal(map[string]any{"assignment": a, "pod_uid": "pod", "worker_epoch": epoch, "nfs_writer_owned": mode != "no_lease", "quarantined": mode == "quarantined" || index == 0,
					"gateways": []gateway.GatewayState{{GatewayID: a.GatewayID, InstanceID: 7, UserID: 1, Generation: 2, State: "running"}}})
				return 200, raw, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			selected, err := p.workbenchSlot(ctx, workbench.Request{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: "gw-7-2"})
			if mode == "hung_neighbor" && time.Since(start) > 2*time.Second {
				t.Fatal("healthy route waited for the caller deadline on another worker")
			}
			if mode == "owned" || mode == "hung_neighbor" {
				if err != nil || selected != 1 {
					t.Fatalf("healthy writer route %d: %v", selected, err)
				}
			} else if err == nil {
				t.Fatal("unproven routing admitted")
			}
		})
	}
}

func TestWorkbenchRechecksNFSWriterAtAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "closed_fd", "replaced_lock", "changed_record", "malformed_record", "old_epoch", "wrong_user", "wrong_generation", "resetting"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			r := gateway.CreateGatewayRequest{InstanceID: 7, UserID: 1, Generation: 2, AgentType: "hermes", PodUID: "pod"}
			a := &Assignment{InstanceID: 7, UserID: 1, Generation: 2, PodUID: r.PodUID, GatewayID: "gw-7-2", State: "starting", WorkerEpoch: "0123456789abcdef0123456789abcdef"}
			w := &Worker{workerEpoch: a.WorkerEpoch, slotName: "slot-1", cfg: gateway.Config{WorkspaceRoot: dir, PodUID: r.PodUID, RuntimeType: r.AgentType, SandboxProfile: "nfs-rootless-v1"}, book: &book{Assignment: a}}
			f, err := w.acquire(r)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			w.lease = f
			req := workbench.Request{InstanceID: 7, UserID: 1, Generation: 2, GatewayID: a.GatewayID}
			switch mode {
			case "closed_fd":
				err = f.Close()
			case "replaced_lock":
				if err = os.Rename(f.Name(), f.Name()+".old"); err == nil {
					err = os.WriteFile(f.Name(), nil, 0600)
				}
			case "changed_record":
				err = stopWriter(w.writerPath, r, w.slotName)
			case "malformed_record":
				err = os.WriteFile(w.writerPath, []byte("{}"), 0600)
			case "old_epoch":
				w.workerEpoch = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "wrong_user":
				req.UserID++
			case "wrong_generation":
				req.Generation++
			case "resetting":
				w.book.StopIntent = &containerStopIntent{}
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.admitsWorkbench(req) != (mode == "valid") {
				t.Fatal("wrong target admission")
			}
		})
	}
}
