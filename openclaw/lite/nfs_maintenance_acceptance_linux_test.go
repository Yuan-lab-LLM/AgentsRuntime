//go:build linux

package openclaw

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netguard"
	"github.com/iamlovingit/clawmanager-agent/slots"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == netguard.RootlessCommand {
		if _, err := netguard.RunRootlessCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "namespace helper failed")
			os.Exit(2)
		}
		return
	}
	if handled, err := gateway.RunNativeStorageHelper(NewProfile("openclaw"), os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "native helper failed")
			os.Exit(2)
		}
		return
	}
	os.Exit(m.Run())
}

func TestNFSNativeFixture(t *testing.T) {
	if os.Getenv("CM_NATIVE_NFS_FIXTURE") != "1" {
		return
	}
	root := os.Getenv("CM_NATIVE_NFS_ROOT")
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", RuntimeType: "openclaw", WorkspaceRoot: root}
	workspace := filepath.Join(root, "openclaw", "user-1", "instance-2")
	req := gateway.CreateGatewayRequest{InstanceID: 2, UserID: 1, UID: 200002, GID: 200002, WorkspacePath: workspace}
	if _, err := gateway.PrepareWorkspaceForConfig(cfg, req); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(workspace, "home", ".openclaw")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(home, "openclaw.json"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"gateway":{"mode":"local"},"agents":{"defaults":{}},"models":{"providers":{}}}`)
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
}

type nativeAcceptanceIntent struct {
	Version          int    `json:"version"`
	OperationID      string `json:"operation_id"`
	PodUID           string `json:"pod_uid"`
	Slot             string `json:"slot"`
	GatewayID        string `json:"gateway_id"`
	InstanceID       int    `json:"instance_id"`
	UserID           int    `json:"user_id"`
	Generation       int    `json:"generation"`
	WorkerEpoch      string `json:"worker_epoch"`
	AssignmentDigest string `json:"assignment_digest"`
	Graceful         bool   `json:"graceful"`
	Kind             string `json:"kind,omitempty"`
}
type nativeAcceptanceMessage struct {
	nativeAcceptanceIntent
	CleanupConfirmed bool `json:"cleanup_confirmed"`
}

func TestNFSNativeMaintenanceAcceptance(t *testing.T) {
	if os.Getenv("CM_NATIVE_NFS_ACCEPTANCE") != "1" {
		t.Skip("requires isolated ordinary-Pod NFS fixture")
	}
	if os.Geteuid() != 10001 || os.Getegid() != 10001 {
		t.Fatal("actual reserved management UID/GID required")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "CapEff:") || strings.HasPrefix(line, "CapInh:") || strings.HasPrefix(line, "CapAmb:") {
			t.Log(line)
		}
	}
	root := os.Getenv("CM_NATIVE_NFS_ROOT")
	state := os.Getenv("CM_NATIVE_NFS_STATE")
	if root == "" || state == "" || os.Getenv("POD_UID") == "" {
		t.Fatal("actual fixture identity required")
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "result.json")
	if _, err := os.Stat(marker); err == nil {
		for {
			time.Sleep(30 * time.Second)
		}
	}
	var operationID, epoch [16]byte
	rand.Read(operationID[:])
	rand.Read(epoch[:])
	intent := nativeAcceptanceIntent{Version: 1, OperationID: hex.EncodeToString(operationID[:]), PodUID: os.Getenv("POD_UID"), Slot: "slot-0", GatewayID: "gw-2-1", InstanceID: 2, UserID: 1, Generation: 1, WorkerEpoch: hex.EncodeToString(epoch[:]), Kind: "maintenance"}
	success := false
	defer func() {
		message := nativeAcceptanceMessage{nativeAcceptanceIntent: intent, CleanupConfirmed: success}
		raw, _ := json.Marshal(message)
		sum := sha256.Sum256(raw)
		commitment := "C1:" + base64.RawURLEncoding.EncodeToString(sum[:])
		record, _ := json.Marshal(map[string]any{"message": message, "commitment": commitment, "success": success})
		if err := os.WriteFile(marker, record, 0600); err != nil {
			t.Log(err)
		}
		file, err := os.OpenFile("/dev/termination-log", os.O_WRONLY|os.O_TRUNC, 0)
		if err == nil {
			_, err = file.WriteString(commitment)
			if err == nil {
				err = file.Sync()
			}
			file.Close()
		}
		if err != nil {
			t.Logf("termination receipt: %v", err)
		}
	}()
	profile := NewProfile("openclaw")
	cfg := gateway.Config{SandboxProfile: "nfs-rootless-v1", NetworkSecurityProfile: "isolated-network-v1", RuntimeType: "openclaw", Runtime: profile, WorkspaceRoot: root, AgentDataDir: state, NetworkSlotIndex: 0, NetworkSlotCount: 1, PodUID: os.Getenv("POD_UID"), OpenClawVersion: "2026.10.2"}
	req := gateway.CreateGatewayRequest{InstanceID: 2, UserID: 1, Generation: 1, AgentType: "openclaw", UID: 200002, GID: 200002, WorkspacePath: filepath.Join(root, "openclaw", "user-1", "instance-2")}
	if err := slots.PrepareInstanceStorage(cfg, req); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	fixture := exec.Command(executable, "-test.run=^TestNFSNativeFixture$")
	fixture.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "CM_NATIVE_NFS_FIXTURE=1", "CM_NATIVE_NFS_ROOT=" + root}
	fixture.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200002, Gid: 200002}}
	if output, err := fixture.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	rollout := "native-acceptance-20261105"
	grant, _ := json.Marshal(gateway.StorageGrantRequest{Root: root, CapsuleRollout: rollout, InstanceID: 2, GID: 200002})
	capsule := exec.Command(executable, "managed-storage-grant")
	capsule.Env = []string{"PATH=/usr/bin:/bin"}
	capsule.Stdin = bytes.NewReader(grant)
	capsule.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10001, Gid: 200002}}
	if output, err := capsule.CombinedOutput(); err != nil {
		t.Fatalf("capsule: %v %s", err, output)
	}
	for _, action := range []string{"inventory", "compatibility"} {
		op := gateway.NativeUpgradeOperation{OperationID: intent.OperationID, Action: action, Request: gateway.WorkspaceUpgradeRequest{RolloutID: rollout, InstanceID: 2, UserID: 1, Generation: 1, UID: 200002, GID: 200002}}
		operation, _ := json.Marshal(op.Child())
		var canonical map[string]any
		json.Unmarshal(operation, &canonical)
		operation, _ = json.Marshal(canonical)
		sum := sha256.Sum256(operation)
		intent.AssignmentDigest = hex.EncodeToString(sum[:])
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		result, err := slots.ExecuteNativeUpgrade(ctx, cfg, op, func(context.Context) error { return nil })
		cancel()
		if err != nil {
			t.Fatalf("native %s: %v", action, err)
		}
		t.Logf("native action=%s result=%s", action, result)
	}
	success = true
}
